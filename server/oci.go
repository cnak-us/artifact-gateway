package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/go-chi/chi/v5"
)

// Deps is the OCI router's dependency bag.
type Deps struct {
	Store    store.DataStore
	Signer   *auth.JWTSigner
	Crypto   *auth.Crypto
	Cache    *license.Cache
	Verifier license.Verifier
	Auditor  *audit.Auditor
	Cfg      *config.Config
	Upstream *Upstream
	Logger   *slog.Logger
	// Revoker drives the row-level revocation check used by BearerJWT. May be
	// nil in narrow test scenarios — production wiring sets it.
	Revoker *TokenRevocationChecker
}

// MountOCI wires /v2/* onto r.
//
//	GET  /v2/       → 401 with Bearer challenge if unauth; 200 {} if auth.
//	GET|POST /v2/token → token mint (no JWT required).
//	*    /v2/<name>/(manifests|blobs|tags)/* → JWT-protected proxy.
//
// The package "name" may contain slashes (e.g. cnak-us/cnak-core), so we
// parse the path manually rather than relying on chi's path-param semantics.
func MountOCI(r chi.Router, d Deps) {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	tokenHandler := &TokenHandler{
		Store:    d.Store,
		Signer:   d.Signer,
		Cache:    d.Cache,
		Verifier: d.Verifier,
		Auditor:  d.Auditor,
		Cfg:      d.Cfg,
		Logger:   d.Logger,
	}

	r.Route("/v2", func(r chi.Router) {
		r.Get("/", handleV2Root(d))
		r.Get("/token", tokenHandler.ServeHTTP)
		r.Post("/token", tokenHandler.ServeHTTP)

		// Everything else is the proxy path.
		r.With(BearerJWT(d.Signer, d.Cfg, d.Revoker)).HandleFunc("/*", handleProxy(d))
	})
}

// handleV2Root implements `/v2/` per the OCI distribution spec. An
// unauthenticated GET advertises the Bearer challenge; an authenticated GET
// returns the empty JSON object the spec mandates.
func handleV2Root(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		challenge := bearerChallenge(d.Cfg.ExternalHostname, "registry:catalog:*")
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(strings.ToLower(authz), "bearer ") {
			w.Header().Set("Www-Authenticate", challenge)
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`)
			return
		}
		if _, err := d.Signer.Verify(strings.TrimSpace(authz[len("Bearer "):])); err != nil {
			w.Header().Set("Www-Authenticate", challenge)
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"errors":[{"code":"UNAUTHORIZED","message":"invalid or expired token"}]}`)
			return
		}
		w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	}
}

// handleProxy resolves the package (and optional container), checks the JWT's
// access grants for the requested action, then hands off to Upstream.Proxy.
func handleProxy(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v2/")
		name, rest, ok := splitOCIPath(path)
		if !ok {
			writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not parseable")
			return
		}

		pkg, container, ok := resolveContainerRequest(r.Context(), d.Store, name)
		if !ok {
			writeOCIError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
			return
		}

		action := actionForRequest(r.Method, rest)
		if !claimsAllow(ClaimsFrom(r.Context()), name, action) {
			challenge := bearerChallenge(d.Cfg.ExternalHostname, fmt.Sprintf("repository:%s:%s", name, action))
			writeBearerChallenge(w, challenge, http.StatusUnauthorized, "insufficient scope")
			return
		}

		d.Upstream.Proxy(w, r, pkg, container, rest)
	}
}

// resolveContainerRequest maps an OCI repo name to a package (+optional
// container). Lookup rules:
//
//  1. Exact-match the full `name` against packages.path. If matched AND the
//     package has no containers (legacy single-container or no children rows),
//     return (pkg, nil, true). If matched but the package has container rows,
//     deny — a multi-container package has no implicit root repo.
//  2. Split `name` on the last '/'. If the prefix matches a package AND an
//     alias row exists for the suffix, return (pkg, container, true).
//  3. Otherwise → not found.
func resolveContainerRequest(ctx context.Context, st store.DataStore, name string) (*store.Package, *store.PackageContainer, bool) {
	if pkg, err := st.GetPackageByPath(ctx, name); err == nil {
		// Multi-container packages have an empty packages.upstream_repo;
		// hitting the bare path is invalid.
		children, _ := st.ListContainersForPackage(ctx, pkg.ID)
		if len(children) == 0 {
			return pkg, nil, true
		}
		return nil, nil, false
	}
	slash := strings.LastIndex(name, "/")
	if slash <= 0 || slash == len(name)-1 {
		return nil, nil, false
	}
	prefix, alias := name[:slash], name[slash+1:]
	pkg, err := st.GetPackageByPath(ctx, prefix)
	if err != nil {
		return nil, nil, false
	}
	container, err := st.GetContainer(ctx, pkg.ID, alias)
	if err != nil {
		return nil, nil, false
	}
	return pkg, container, true
}

// splitOCIPath parses `<name>/(manifests|blobs|tags)/<reference...>` from the
// path-portion (already trimmed of the `/v2/` prefix). Returns name, the
// remainder (starting with `/manifests|blobs|tags`), and ok.
func splitOCIPath(p string) (name, rest string, ok bool) {
	for _, kw := range []string{"/manifests/", "/blobs/", "/tags/"} {
		if i := strings.Index(p, kw); i > 0 {
			return p[:i], p[i:], true
		}
	}
	return "", "", false
}

// actionForRequest returns the OCI action (pull|push|delete) implied by the
// HTTP method and sub-path. Today we only proxy reads, so anything non-GET/HEAD
// maps to push and is rejected by the grants check.
func actionForRequest(method, rest string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return "pull"
	case http.MethodDelete:
		return "delete"
	default:
		return "push"
	}
}

// claimsAllow returns true if the JWT grants `action` on `repo`.
func claimsAllow(claims *auth.OCIClaims, repo, action string) bool {
	if claims == nil {
		return false
	}
	for _, a := range claims.Access {
		if a.Type != "repository" || a.Name != repo {
			continue
		}
		for _, act := range a.Actions {
			if act == action || act == "*" {
				return true
			}
		}
	}
	return false
}

func writeOCIError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":%q}]}`, code, msg)
}
