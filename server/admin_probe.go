package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	agoidc "github.com/cnak-us/artifact-gateway/oidc"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	probeTimeout    = 10 * time.Second
	probeBodyCapKB  = 32
	probeBodyCap    = probeBodyCapKB * 1024
	probeUserAgent  = "artifact-gateway/1.0"
	probeAPIVersion = "2022-11-28"
)

// probeResult mirrors the JSON returned to the admin UI for both probe endpoints.
// `Body` is `json.RawMessage` for JSON upstream responses (preserves ordering),
// or a JSON-encoded string for non-JSON / truncated / unparseable bodies.
type probeResult struct {
	OK         bool              `json:"ok"`
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Status     int               `json:"status"`
	StatusText string            `json:"status_text"`
	DurationMS int64             `json:"duration_ms"`
	Summary    string            `json:"summary"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       json.RawMessage   `json:"body,omitempty"`
	BodyIsJSON bool              `json:"body_is_json"`
}

var probeHeaderWhitelist = []string{
	"Content-Type",
	"X-OAuth-Scopes",
	"X-Accepted-OAuth-Scopes",
	"X-RateLimit-Limit",
	"X-RateLimit-Remaining",
	"X-RateLimit-Reset",
	"X-RateLimit-Used",
	"X-RateLimit-Resource",
	"X-GitHub-Media-Type",
	"Www-Authenticate",
	"Docker-Distribution-Api-Version",
	"Retry-After",
	"Date",
	"Server",
}

func pickHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(probeHeaderWhitelist))
	for _, k := range probeHeaderWhitelist {
		if v := h.Get(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// readProbeBody reads up to probeBodyCap bytes, returning the body as
// json.RawMessage. If the content-type advertises JSON and the body parses
// cleanly within the cap, the raw JSON is returned with bodyIsJSON=true.
// Otherwise the body is returned as a JSON-encoded string and bodyIsJSON=false.
// Bodies longer than the cap get a trailing "... [truncated]" marker.
func readProbeBody(resp *http.Response) (json.RawMessage, bool) {
	limited := io.LimitReader(resp.Body, int64(probeBodyCap)+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return mustJSONString(""), false
	}
	truncated := len(raw) > probeBodyCap
	if truncated {
		raw = raw[:probeBodyCap]
	}

	ct := resp.Header.Get("Content-Type")
	isJSON := strings.Contains(strings.ToLower(ct), "json")
	if isJSON && !truncated && json.Valid(raw) {
		return json.RawMessage(raw), true
	}
	body := string(raw)
	if truncated {
		body += "... [truncated]"
	}
	return mustJSONString(body), false
}

func mustJSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func newProbeClient() *http.Client {
	return &http.Client{Timeout: probeTimeout}
}

// --- upstream credential test ----------------------------------------------

func testUpstreamCred(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		cred, err := d.Store.GetUpstreamCredential(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "credential not found")
			return
		}
		pat, err := d.Crypto.Open(cred.PATEnc)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "decrypt failed")
			return
		}

		var res probeResult
		switch cred.Kind {
		case "github-api":
			res = probeGitHubUser(r, string(pat))
		case "ghcr":
			res = probeGHCRRegistry(r, cred.Username, string(pat))
		case "oci-basic":
			res = probeOCIBasic(r, cred, string(pat))
		case "dockerhub", "quay", "gitlab":
			res = probeBearerExchange(r, cred, string(pat))
		case "ecr", "gar", "acr-aad":
			res = probeIssuerMint(r, d, cred)
		case "gitlab-api":
			res = probeGitLabUser(r, cred, string(pat))
		default:
			writeJSONErr(w, http.StatusBadRequest, "unsupported credential kind")
			return
		}

		_ = d.Store.TouchUpstreamCredential(r.Context(), cred.ID)
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "probe", "upstream-credential", cred.ID.String(), cred.Name, clientIP(r))
		writeJSON(w, http.StatusOK, res)
	}
}

func probeGitHubUser(r *http.Request, pat string) probeResult {
	url := "https://api.github.com/user"
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", probeAPIVersion)
	req.Header.Set("User-Agent", probeUserAgent)

	resp, err := newProbeClient().Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	body, isJSON := readProbeBody(resp)
	headers := pickHeaders(resp.Header)
	res := probeResult{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    headers,
		Body:       body,
		BodyIsJSON: isJSON,
	}

	if res.OK && isJSON {
		var u struct {
			Login string `json:"login"`
		}
		_ = json.Unmarshal(body, &u)
		scopes := resp.Header.Get("X-OAuth-Scopes")
		if scopes == "" {
			scopes = "(none)"
		}
		limit := resp.Header.Get("X-RateLimit-Limit")
		rem := resp.Header.Get("X-RateLimit-Remaining")
		rate := ""
		if limit != "" && rem != "" {
			rate = fmt.Sprintf(" Rate limit: %s/%s.", rem, limit)
		}
		login := u.Login
		if login == "" {
			login = "(unknown)"
		}
		res.Summary = fmt.Sprintf("Authenticated as %s. Scopes: %s.%s", login, scopes, rate)
	} else {
		res.Summary = fmt.Sprintf("GitHub /user returned %s", resp.Status)
	}
	return res
}

// probeOCIBasic hits /v2/ on the credential's BaseURL with Basic auth and
// reports success when the upstream returns 2xx or a 401 with a Bearer
// challenge (a 401-with-WWW-Authenticate means the registry is reachable
// and is asking for a token exchange — bucket B; for oci-basic the user
// will see this and know they hit a token-exchange registry by mistake).
func probeOCIBasic(r *http.Request, cred *store.UpstreamCredential, pat string) probeResult {
	url := strings.TrimRight(cred.BaseURL, "/") + "/v2/"
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	basic := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + pat))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("User-Agent", probeUserAgent)

	client := newProbeClientForCred(cred)
	resp, err := client.Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	body, isJSON := readProbeBody(resp)
	headers := pickHeaders(resp.Header)
	is2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
	is401WithChallenge := resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("Www-Authenticate") != ""

	res := probeResult{
		OK:         is2xx || is401WithChallenge,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    headers,
		Body:       body,
		BodyIsJSON: isJSON,
	}
	switch {
	case is2xx:
		res.Summary = "OCI registry responded with " + resp.Status
	case is401WithChallenge:
		res.Summary = "Registry returned 401 with a Bearer challenge — this looks like a token-exchange registry (e.g. Docker Hub, Quay, GitLab). Use a kind with bearer-exchange support instead of oci-basic."
	default:
		res.Summary = "OCI registry returned " + resp.Status
	}
	return res
}

// probeBearerExchange tests a bucket-B credential (dockerhub/quay/gitlab):
// hit /v2/, observe the WWW-Authenticate: Bearer challenge, mint a token
// via the realm with the stored PAT, and re-issue /v2/ with the bearer.
// Success = either an immediate 2xx (no auth required) or a successful
// realm exchange followed by a 2xx retry.
func probeBearerExchange(r *http.Request, cred *store.UpstreamCredential, pat string) probeResult {
	host := effectiveHost(cred)
	if host == "" {
		return probeResult{OK: false, Summary: "no base URL resolved for credential"}
	}
	url := host + "/v2/"
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	req.Header.Set("User-Agent", probeUserAgent)

	client := newProbeClientForCred(cred)
	resp, err := client.Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		challengeHeader := resp.Header.Get("Www-Authenticate")
		_ = resp.Body.Close()
		ch := parseBearerChallenge(challengeHeader)
		if ch == nil {
			return probeResult{
				OK: false, URL: url, Method: http.MethodGet, Status: resp.StatusCode,
				StatusText: resp.Status,
				DurationMS: time.Since(start).Milliseconds(),
				Summary:    "registry returned 401 but no Bearer challenge — wrong Kind for this upstream?",
			}
		}
		bearer := &BearerExchangeAuthenticator{HTTPClient: client}
		token, _, err := bearer.mintToken(r.Context(), ch, cred, []byte(pat))
		if err != nil {
			return probeResult{
				OK: false, URL: url, Method: http.MethodGet, Status: resp.StatusCode,
				StatusText: resp.Status,
				DurationMS: time.Since(start).Milliseconds(),
				Summary:    "realm exchange failed: " + err.Error(),
			}
		}
		req2, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
		req2.Header.Set("Authorization", "Bearer "+token)
		req2.Header.Set("User-Agent", probeUserAgent)
		resp, err = client.Do(req2)
		if err != nil {
			return probeResult{
				OK: false, URL: url, Method: http.MethodGet, Status: 0,
				DurationMS: time.Since(start).Milliseconds(),
				Summary:    "retry-with-bearer failed: " + err.Error(),
			}
		}
	}
	defer resp.Body.Close()

	body, isJSON := readProbeBody(resp)
	res := probeResult{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    pickHeaders(resp.Header),
		Body:       body,
		BodyIsJSON: isJSON,
	}
	if res.OK {
		res.Summary = "Bearer exchange succeeded; /v2/ returned " + resp.Status
	} else {
		res.Summary = "Bearer exchange round-trip ended in " + resp.Status
	}
	return res
}

// probeGitLabUser exercises a gitlab-api credential by calling
// /api/v4/user — the canonical "who am I" endpoint. Mirrors probeGitHubUser.
func probeGitLabUser(r *http.Request, cred *store.UpstreamCredential, pat string) probeResult {
	url := gitlabAPIBase(cred.BaseURL) + "/user"
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	req.Header.Set("PRIVATE-TOKEN", pat)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", probeUserAgent)

	resp, err := newProbeClientForCred(cred).Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	body, isJSON := readProbeBody(resp)
	res := probeResult{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    pickHeaders(resp.Header),
		Body:       body,
		BodyIsJSON: isJSON,
	}
	if res.OK && isJSON {
		var u struct {
			Username string `json:"username"`
		}
		_ = json.Unmarshal(body, &u)
		login := u.Username
		if login == "" {
			login = "(unknown)"
		}
		res.Summary = fmt.Sprintf("Authenticated as %s on %s.", login, cred.BaseURL)
	} else {
		res.Summary = fmt.Sprintf("GitLab /user returned %s", resp.Status)
	}
	return res
}

// probeGitLabReleases hits /projects/:id/releases with a low per_page so the
// caller can verify a package's Releases path resolves under the credential.
func probeGitLabReleases(r *http.Request, pkg *store.Package, cred *store.UpstreamCredential, pat string) probeResult {
	url := fmt.Sprintf("%s/projects/%s/releases?per_page=5",
		gitlabAPIBase(cred.BaseURL), projectPathID(pkg.GitHubRepo))
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	req.Header.Set("PRIVATE-TOKEN", pat)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", probeUserAgent)

	resp, err := newProbeClientForCred(cred).Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}
	defer resp.Body.Close()
	body, isJSON := readProbeBody(resp)
	res := probeResult{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    pickHeaders(resp.Header),
		Body:       body,
		BodyIsJSON: isJSON,
	}
	if res.OK && isJSON {
		var rels []struct {
			TagName string `json:"tag_name"`
		}
		_ = json.Unmarshal(body, &rels)
		switch len(rels) {
		case 0:
			res.Summary = "No releases visible on this project."
		case 1:
			res.Summary = fmt.Sprintf("Reachable. Newest release: %s.", rels[0].TagName)
		default:
			res.Summary = fmt.Sprintf("Reachable. %d releases visible (newest: %s).", len(rels), rels[0].TagName)
		}
	} else {
		res.Summary = fmt.Sprintf("GitLab /releases returned %s", resp.Status)
	}
	return res
}

// probeIssuerMint validates a bucket-C credential by minting a registry
// token via the configured IssuerMintAuthenticator (ECR/GAR/ACR-AAD) and
// then hitting the credential's resolved /v2/ endpoint to confirm the
// minted Authorization header is accepted.
func probeIssuerMint(r *http.Request, d AdminDeps, cred *store.UpstreamCredential) probeResult {
	start := time.Now()
	if d.Upstream == nil || d.Upstream.IssuerAuth == nil {
		return probeResult{OK: false, Summary: "issuer-mint authenticator not wired"}
	}
	host := effectiveHost(cred)
	if host == "" {
		return probeResult{OK: false, Summary: "could not resolve registry host for credential"}
	}
	url := host + "/v2/"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	if err := d.Upstream.IssuerAuth.Apply(r.Context(), req, cred, nil); err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "mint failed: " + err.Error(),
		}
	}
	req.Header.Set("User-Agent", probeUserAgent)
	resp, err := newProbeClientForCred(cred).Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}
	defer resp.Body.Close()
	body, isJSON := readProbeBody(resp)
	is2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
	res := probeResult{
		OK:         is2xx,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    pickHeaders(resp.Header),
		Body:       body,
		BodyIsJSON: isJSON,
	}
	if is2xx {
		res.Summary = "Mint succeeded; /v2/ returned " + resp.Status
	} else {
		res.Summary = "Mint succeeded but /v2/ returned " + resp.Status
	}
	return res
}

// newProbeClientForCred builds a probe-timeout http.Client honoring the
// credential's TLS settings. Mirrors Upstream.clientFor but without the
// no-redirect-follow policy (probes are short-lived diagnostic calls).
func newProbeClientForCred(cred *store.UpstreamCredential) *http.Client {
	if cred.CABundlePEM == "" && !cred.InsecureSkipTLSVerify {
		return newProbeClient()
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cred.InsecureSkipTLSVerify}
	if cred.CABundlePEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM([]byte(cred.CABundlePEM))
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		Timeout:   probeTimeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}

func probeGHCRRegistry(r *http.Request, username, pat string) probeResult {
	url := "https://ghcr.io/v2/"
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	basic := base64.StdEncoding.EncodeToString([]byte(username + ":" + pat))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("User-Agent", probeUserAgent)

	resp, err := newProbeClient().Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	body, isJSON := readProbeBody(resp)
	headers := pickHeaders(resp.Header)
	is2xx := resp.StatusCode >= 200 && resp.StatusCode < 300
	is401WithChallenge := resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("Www-Authenticate") != ""

	res := probeResult{
		OK:         is2xx || is401WithChallenge,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    headers,
		Body:       body,
		BodyIsJSON: isJSON,
	}
	switch {
	case is401WithChallenge:
		res.Summary = "GHCR registry responded with 401 (expected — registry will request a token per pull)"
	case is2xx:
		res.Summary = "GHCR registry responded with " + resp.Status
	default:
		res.Summary = "GHCR registry returned " + resp.Status
	}
	return res
}

// --- package probe ----------------------------------------------------------

func probePackage(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		pkg, err := d.Store.GetPackage(r.Context(), id)
		if err != nil {
			writeJSONErr(w, http.StatusNotFound, "package not found")
			return
		}
		cred, err := d.Store.GetUpstreamCredential(r.Context(), pkg.UpstreamCredentialID)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "bound upstream credential not found")
			return
		}
		pat, err := d.Crypto.Open(cred.PATEnc)
		if err != nil {
			writeJSONErr(w, http.StatusInternalServerError, "decrypt failed")
			return
		}

		var res probeResult
		switch pkg.Source {
		case "gitlab-release":
			res = probeGitLabReleases(r, pkg, cred, string(pat))
		case "github-release":
			res = probeGitHubReleases(r, pkg, string(pat))
		case "oci", "":
			res = probeOCITagList(r, pkg, cred, string(pat))
		default:
			writeJSONErr(w, http.StatusBadRequest, "unsupported package source")
			return
		}

		_ = d.Store.TouchUpstreamCredential(r.Context(), cred.ID)
		s := agoidc.SessionFrom(r.Context())
		d.Auditor.LogResourceMutation(actorEmail(s), "probe", "package", pkg.ID.String(), pkg.Slug, clientIP(r))
		writeJSON(w, http.StatusOK, res)
	}
}

func probeGitHubReleases(r *http.Request, pkg *store.Package, pat string) probeResult {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=5", pkg.GitHubRepo)
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return probeResult{OK: false, URL: url, Method: http.MethodGet, Summary: "request build failed: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", probeAPIVersion)
	req.Header.Set("User-Agent", probeUserAgent)

	resp, err := newProbeClient().Do(req)
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	body, isJSON := readProbeBody(resp)
	headers := pickHeaders(resp.Header)
	res := probeResult{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    headers,
		Body:       body,
		BodyIsJSON: isJSON,
	}

	if res.OK && isJSON {
		var rels []struct {
			TagName string `json:"tag_name"`
		}
		_ = json.Unmarshal(body, &rels)
		switch len(rels) {
		case 0:
			res.Summary = "0 releases returned (repository has no published releases yet)"
		default:
			res.Summary = fmt.Sprintf("%d releases returned. Newest tag: %s", len(rels), rels[0].TagName)
		}
	} else {
		res.Summary = fmt.Sprintf("GitHub releases returned %s", resp.Status)
	}
	return res
}

func probeOCITagList(r *http.Request, pkg *store.Package, cred *store.UpstreamCredential, pat string) probeResult {
	host := effectiveHost(cred)
	if host == "" {
		return probeResult{OK: false, Method: http.MethodGet, Summary: "no base URL resolved for credential"}
	}
	url := fmt.Sprintf("%s/v2/%s/tags/list?n=100", host, pkg.UpstreamRepo)
	client := newProbeClientForCred(cred)
	start := time.Now()

	resp, err := probeTagListGet(r.Context(), client, url, "Basic "+base64.StdEncoding.EncodeToString([]byte(cred.Username+":"+pat)))
	if err != nil {
		return probeResult{
			OK: false, URL: url, Method: http.MethodGet, Status: 0,
			DurationMS: time.Since(start).Milliseconds(),
			Summary:    "network error: " + err.Error(),
		}
	}

	// If the registry rejects Basic with a Bearer challenge (ghcr.io always
	// does this for tags/list; Harbor/Gitea/oci-basic registries usually
	// accept Basic and never get here), mint a scope-pinned token via the
	// challenge's realm and retry. We force the scope to the actual repo —
	// ghcr.io's challenge returns the placeholder "repository:user/image:pull"
	// regardless of the request path, so trusting the challenge scope would
	// mint a useless token.
	if resp.StatusCode == http.StatusUnauthorized {
		if ch := parseBearerChallenge(resp.Header.Get("Www-Authenticate")); ch != nil {
			_ = resp.Body.Close()
			ch.Scope = fmt.Sprintf("repository:%s:pull", pkg.UpstreamRepo)
			bearer := &BearerExchangeAuthenticator{HTTPClient: client}
			token, _, mintErr := bearer.mintToken(r.Context(), ch, cred, []byte(pat))
			if mintErr != nil {
				return probeResult{
					OK: false, URL: url, Method: http.MethodGet,
					Status: http.StatusUnauthorized, StatusText: "401 Unauthorized",
					DurationMS: time.Since(start).Milliseconds(),
					Summary:    "bearer exchange failed: " + mintErr.Error(),
				}
			}
			resp, err = probeTagListGet(r.Context(), client, url, "Bearer "+token)
			if err != nil {
				return probeResult{
					OK: false, URL: url, Method: http.MethodGet, Status: 0,
					DurationMS: time.Since(start).Milliseconds(),
					Summary:    "retry-with-bearer failed: " + err.Error(),
				}
			}
		}
	}
	defer resp.Body.Close()

	body, isJSON := readProbeBody(resp)
	headers := pickHeaders(resp.Header)
	res := probeResult{
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		URL:        url,
		Method:     http.MethodGet,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		DurationMS: time.Since(start).Milliseconds(),
		Headers:    headers,
		Body:       body,
		BodyIsJSON: isJSON,
	}

	if res.OK && isJSON {
		var tl struct {
			Tags []string `json:"tags"`
		}
		_ = json.Unmarshal(body, &tl)
		switch len(tl.Tags) {
		case 0:
			res.Summary = "0 tags returned"
		default:
			res.Summary = fmt.Sprintf("%d tags returned. Newest tag: %s", len(tl.Tags), tl.Tags[len(tl.Tags)-1])
		}
	} else {
		res.Summary = fmt.Sprintf("OCI tags/list returned %s", resp.Status)
	}
	return res
}

func probeTagListGet(ctx context.Context, client *http.Client, url, authHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("User-Agent", probeUserAgent)
	return client.Do(req)
}
