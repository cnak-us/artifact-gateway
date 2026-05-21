package server

import (
	"net/http"
	"strings"

	"github.com/cnak-us/artifact-gateway/metrics"
	agoidc "github.com/cnak-us/artifact-gateway/oidc"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)


var uuidNil = uuid.Nil

// MountCatalogOIDC wires the customer-facing OAuth flow.
//
//   GET /catalog/oidc-providers          — public listing of enabled providers
//   GET /catalog/oidc/:provider/start    — kick off auth-code flow
//
// The OAuth `redirect_uri` is the single canonical /api/v1/auth/oidc/<name>/
// callback URL (see oidc/provider.go) — operators only need to register one
// URL per IdP. The shared callback dispatches to the catalog hook installed
// below whenever the state cookie carries Flow=="customer", so the customer
// flow lands back on /catalog with an ag_customer_session cookie.
func MountCatalogOIDC(r chi.Router, d CatalogDeps, oidcDeps *agoidc.HandlerDeps, registry *agoidc.Registry) {
	// Install the customer hook on the shared HandlerDeps. The hook owns
	// customer-session issuance — keeps the oidc package free of license/store
	// imports. Dex authentication alone is sufficient to issue a session;
	// license entitlement is enforced at package list/pull time.
	if oidcDeps != nil && oidcDeps.CustomerCallback == nil {
		oidcDeps.CustomerCallback = customerOIDCSessionHook(d)
	}

	// Public list of enabled providers — the catalog login page calls this.
	r.Get("/catalog/oidc-providers", func(w http.ResponseWriter, r *http.Request) {
		type provider struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			IsDefault   bool   `json:"is_default"`
		}
		names := registry.Names()
		out := make([]provider, 0, len(names))
		for _, n := range names {
			out = append(out, provider{
				Name:        n,
				DisplayName: strings.ToLower(n),
				IsDefault:   n == d.OIDCDefaultProvider,
			})
		}
		writeJSON(w, http.StatusOK, out)
	})

	// Start the auth-code flow tagged for the customer path. StartCustomer
	// embeds Flow="customer" in the signed state cookie so the shared admin
	// callback URL knows to invoke the hook above.
	r.Get("/catalog/oidc/{provider}/start", func(w http.ResponseWriter, r *http.Request) {
		oidcDeps.StartCustomer(w, r, chi.URLParam(r, "provider"))
	})
}

// customerOIDCSessionHook is the post-exchange handler for Flow=="customer".
// Invoked from agoidc.HandlerDeps.Callback after a successful code exchange
// and email extraction. Owns:
//   - issuing the customer session (ag_customer_session, Role=customer,
//     UserID=uuid.Nil — resolveLicenseForSession re-derives any matching
//     license from license_contacts on each request)
//   - audit + metrics labelling as "oidc-catalog"
//   - redirect to the validated returnTo (defaults to /catalog)
//
// Dex authentication alone is enough to issue a session. Users whose email
// isn't on any license contact list still get a working catalog session;
// they just see an empty catalog. License entitlement is enforced at
// package list/pull time, not here.
func customerOIDCSessionHook(d CatalogDeps) func(http.ResponseWriter, *http.Request, string, string, string, string) {
	return func(w http.ResponseWriter, r *http.Request, providerName, email, _subject, returnTo string) {
		ip := clientIP(r)

		if err := d.Sessions.Issue(w, agoidc.Session{
			UserID: uuidNil,
			Email:  email,
			Role:   "customer",
		}); err != nil {
			http.Error(w, "session issue failed", http.StatusInternalServerError)
			return
		}
		metrics.AdminLoginsTotal.WithLabelValues("oidc-catalog", "success").Inc()
		d.Auditor.LogAdminLogin(email, "oidc-catalog:"+providerName, ip, "success")

		if returnTo == "" {
			returnTo = "/catalog"
		}
		http.Redirect(w, r, returnTo, http.StatusFound)
	}
}
