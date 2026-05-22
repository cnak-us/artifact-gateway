package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/metrics"
	"github.com/cnak-us/artifact-gateway/store"
)

// Upstream is the reverse-proxy half of the gateway. It rewrites a customer
// request against `/v2/<gateway-path>/...` to the upstream registry's path,
// injects the stored credential via an UpstreamAuthenticator, and forwards
// relevant headers.
//
// Blob responses must NOT be followed: ghcr issues a 307 to a signed
// githubusercontent URL that the client fetches directly, so we copy the 307
// through to the client (no PAT, no proxy bandwidth).
type Upstream struct {
	Client      *http.Client
	Crypto      *auth.Crypto
	Store       store.DataStore
	Auditor     *audit.Auditor
	Logger      *slog.Logger
	clientCache *clientCache
	// IssuerAuth is the bucket-C authenticator wired with Crypto for
	// decrypting issuer secrets. NewUpstream constructs it and registers
	// it for ecr/gar/acr-aad Kinds.
	IssuerAuth *IssuerMintAuthenticator
}

// NewUpstream constructs an Upstream. The HTTP client is configured to NOT
// follow redirects so blob 307s pass through verbatim.
func NewUpstream(crypto *auth.Crypto, st store.DataStore, auditor *audit.Auditor, logger *slog.Logger) *Upstream {
	if logger == nil {
		logger = slog.Default()
	}
	c := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	issuerAuth := NewIssuerMintAuthenticator(crypto)
	for _, k := range IssuerKinds {
		RegisterAuthenticator(k, issuerAuth)
	}
	return &Upstream{
		Client:      c,
		Crypto:      crypto,
		Store:       st,
		Auditor:     auditor,
		Logger:      logger,
		clientCache: newClientCache(),
		IssuerAuth:  issuerAuth,
	}
}

// StartIssuerRefresh launches the background refresh loop for short-lived
// bucket-C tokens (ECR / GAR / ACR-AAD). Cancel the context to stop.
func (u *Upstream) StartIssuerRefresh(ctx context.Context) {
	if u.IssuerAuth != nil {
		u.IssuerAuth.startRefreshLoop(ctx, u.Store)
	}
}

// Proxy rewrites and forwards a single OCI request.
// gatewayPath is the package path as seen by the client (e.g. "cnak-us/cnak-core").
// container is nil for legacy single-container packages; when non-nil, its
// UpstreamRepo overrides pkg.UpstreamRepo. The upstream credential always
// comes from the package row — one credential is shared across all containers.
// rest is everything after that path — "/manifests/v1", "/blobs/sha256:...", "/tags/list".
func (u *Upstream) Proxy(w http.ResponseWriter, r *http.Request, pkg *store.Package, container *store.PackageContainer, rest string) {
	metrics.ProxyInFlight.Inc()
	defer metrics.ProxyInFlight.Dec()

	endpoint := classifyEndpoint(rest)

	cred, err := u.Store.GetUpstreamCredential(r.Context(), pkg.UpstreamCredentialID)
	if err != nil {
		metrics.UpstreamErrorsTotal.WithLabelValues("credential_missing").Inc()
		u.Logger.Error("upstream credential lookup failed", "err", err, "package", pkg.Path)
		http.Error(w, "upstream credential unavailable", http.StatusBadGateway)
		return
	}
	pat, err := u.Crypto.Open(cred.PATEnc)
	if err != nil {
		metrics.UpstreamErrorsTotal.WithLabelValues("credential_decrypt").Inc()
		u.Logger.Error("upstream PAT decrypt failed", "err", err, "credential", cred.Name)
		http.Error(w, "upstream credential unavailable", http.StatusBadGateway)
		return
	}

	upstreamRepo := pkg.UpstreamRepo
	if container != nil {
		upstreamRepo = container.UpstreamRepo
	}
	host := effectiveHost(cred)
	upstreamURL := host + "/v2/" + upstreamRepo + rest
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}

	auth := authenticatorFor(cred.Kind)
	client := u.clientFor(cred)

	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(r.Context(), method, upstreamURL, http.NoBody)
		if err != nil {
			return nil, err
		}
		copyHeaderIfSet(req.Header, r.Header, "Accept")
		copyHeaderIfSet(req.Header, r.Header, "Accept-Encoding")
		copyHeaderIfSet(req.Header, r.Header, "Range")
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "artifact-gateway/1.0")
		}
		if err := auth.Apply(r.Context(), req, cred, pat); err != nil {
			return nil, err
		}
		return req, nil
	}

	req, err := buildReq()
	if err != nil {
		metrics.UpstreamErrorsTotal.WithLabelValues("build_request").Inc()
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			metrics.UpstreamErrorsTotal.WithLabelValues("client_cancel").Inc()
		} else {
			metrics.UpstreamErrorsTotal.WithLabelValues("network").Inc()
		}
		u.Logger.Warn("upstream request failed", "err", err, "url", upstreamURL)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	if resp.StatusCode == http.StatusUnauthorized {
		retry, hookErr := auth.OnUnauthorized(r.Context(), resp, cred, pat)
		if hookErr != nil {
			_ = resp.Body.Close()
			metrics.UpstreamErrorsTotal.WithLabelValues("auth_refresh").Inc()
			u.Logger.Warn("upstream auth refresh failed", "err", hookErr, "credential", cred.Name)
			http.Error(w, "upstream auth unavailable", http.StatusBadGateway)
			return
		}
		if retry {
			_ = resp.Body.Close()
			req2, err := buildReq()
			if err != nil {
				metrics.UpstreamErrorsTotal.WithLabelValues("build_request").Inc()
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			resp, err = client.Do(req2)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					metrics.UpstreamErrorsTotal.WithLabelValues("client_cancel").Inc()
				} else {
					metrics.UpstreamErrorsTotal.WithLabelValues("network").Inc()
				}
				u.Logger.Warn("upstream request failed after refresh", "err", err, "url", upstreamURL)
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
				return
			}
		}
	}
	defer resp.Body.Close()
	metrics.UpstreamRequestLatency.WithLabelValues(endpoint, strconv.Itoa(resp.StatusCode)).Observe(time.Since(start).Seconds())

	switch endpoint {
	case "blob":
		u.proxyBlob(w, resp)
	case "manifest":
		u.proxyManifest(w, resp, r.Method)
	default:
		u.proxyGeneric(w, resp, endpoint)
	}

	// Async touch on success.
	if resp.StatusCode < 400 {
		credID := cred.ID
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = u.Store.TouchUpstreamCredential(bg, credID)
		}()
	}
	if r.Method == http.MethodGet && (endpoint == "manifest" || endpoint == "blob") && u.Auditor != nil {
		claims := ClaimsFrom(r.Context())
		subj := ""
		if claims != nil {
			subj = claims.Subject
		}
		auditPath := pkg.Path
		if container != nil {
			auditPath = pkg.Path + "/" + container.Alias
		}
		u.Auditor.LogPackagePull(subj, auditPath, rest, clientIP(r))
	}
}

// proxyBlob copies a blob response straight through. We expect a 307 with a
// `Location` to githubusercontent.com — copy it unchanged so the client fetches
// the signed URL directly. For 200/206 (registry that doesn't redirect), stream
// the body so the client still gets bytes.
func (u *Upstream) proxyBlob(w http.ResponseWriter, resp *http.Response) {
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		copyHeaderIfSet(w.Header(), resp.Header, "Location")
		copyHeaderIfSet(w.Header(), resp.Header, "Docker-Content-Digest")
		copyHeaderIfSet(w.Header(), resp.Header, "Content-Length")
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if n, err := strconv.ParseFloat(cl, 64); err == nil {
				metrics.BlobRedirectBytesTotal.Add(n)
			}
		}
		w.WriteHeader(resp.StatusCode)
		metrics.BlobRequestsTotal.WithLabelValues("redirect").Inc()
		return
	}
	if resp.StatusCode >= 400 {
		metrics.BlobRequestsTotal.WithLabelValues("error").Inc()
	} else {
		metrics.BlobRequestsTotal.WithLabelValues("proxied").Inc()
	}
	copyResponse(w, resp)
}

// proxyManifest copies a manifest response, preserving the headers a client
// needs to verify the content.
func (u *Upstream) proxyManifest(w http.ResponseWriter, resp *http.Response, method string) {
	result := "ok"
	if resp.StatusCode >= 400 {
		result = "error"
	}
	metrics.ManifestRequestsTotal.WithLabelValues(method, result).Inc()
	copyResponse(w, resp)
}

// proxyGeneric handles tag lists and anything else we don't special-case.
func (u *Upstream) proxyGeneric(w http.ResponseWriter, resp *http.Response, endpoint string) {
	copyResponse(w, resp)
}

// copyResponse copies the status, the meaningful headers, and the body of an
// upstream response to the client.
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	for _, h := range []string{
		"Content-Type",
		"Content-Length",
		"Docker-Content-Digest",
		"Docker-Distribution-API-Version",
		"Etag",
		"Last-Modified",
		"Link",
	} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func copyHeaderIfSet(dst, src http.Header, name string) {
	if v := src.Get(name); v != "" {
		dst.Set(name, v)
	}
}

// classifyEndpoint maps a sub-path (after the package path) to one of
// manifest|blob|tags|other for metrics and per-endpoint handling.
func classifyEndpoint(rest string) string {
	switch {
	case strings.HasPrefix(rest, "/manifests/"):
		return "manifest"
	case strings.HasPrefix(rest, "/blobs/"):
		return "blob"
	case rest == "/tags/list" || strings.HasPrefix(rest, "/tags/list?"):
		return "tags"
	default:
		return "other"
	}
}
