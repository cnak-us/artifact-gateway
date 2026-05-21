// Package metrics defines Prometheus metrics for artifact-gateway.
// All metrics use the "artifact_gateway" namespace.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const ns = "artifact_gateway"

var (
	TokenMintsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "token_mints_total",
			Help:      "OCI bearer token mint attempts.",
		},
		[]string{"result"}, // success|denied_license|denied_scope|unauthorized
	)

	TokenMintLatency = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "token_mint_latency_seconds",
			Help:      "End-to-end latency for /v2/token.",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1},
		},
	)

	ManifestRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "manifest_requests_total",
			Help:      "Manifest GET/HEAD requests.",
		},
		[]string{"method", "result"},
	)

	BlobRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "blob_requests_total",
			Help:      "Blob requests by outcome.",
		},
		[]string{"outcome"}, // redirect|proxied|error
	)

	BlobRedirectBytesTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "blob_redirect_bytes_total",
			Help:      "Bytes implied by redirected blobs (from upstream Content-Length).",
		},
	)

	UpstreamRequestLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "upstream_request_latency_seconds",
			Help:      "Latency of upstream registry calls.",
			Buckets:   []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5},
		},
		[]string{"endpoint", "code"},
	)

	UpstreamErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "upstream_errors_total",
			Help:      "Upstream registry errors.",
		},
		[]string{"kind"},
	)

	LicenseCheckFailuresTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "license_check_failures_total",
			Help:      "License checks that failed during a token mint.",
		},
		[]string{"reason"}, // expired|revoked|parse_error|sig_invalid|missing
	)

	CustomerTokensActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "customer_tokens_active",
			Help:      "Active (non-revoked, non-expired) customer tokens.",
		},
	)

	AdminLoginsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "admin_logins_total",
			Help:      "Admin login attempts.",
		},
		[]string{"method", "result"},
	)

	ProxyInFlight = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "proxy_in_flight",
			Help:      "In-flight proxied requests.",
		},
	)

	AuditEventsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "audit_events_total",
			Help:      "Audit events emitted by resource_type and action.",
		},
		[]string{"resource_type", "action"},
	)

	DownloadsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "downloads_total",
			Help:      "Download attempts by source and outcome.",
		},
		[]string{"source", "outcome"},
	)

	DownloadSignedURLsIssuedTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "download_signed_urls_issued_total",
			Help:      "Short-lived signed download URLs issued for browser clicks.",
		},
	)

	GitHubAPIRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "github_api_requests_total",
			Help:      "Outbound calls to the GitHub REST API by result.",
		},
		[]string{"result"},
	)

	GitHubAPIRateLimitRemaining = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "github_api_rate_limit_remaining",
			Help:      "Most-recently observed X-RateLimit-Remaining from GitHub.",
		},
	)

	GitLabAPIRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "gitlab_api_requests_total",
			Help:      "Outbound calls to the GitLab REST API by result.",
		},
		[]string{"result"},
	)
)

// Handler returns the Prometheus HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Init pre-touches every known label combination so each metric appears in
// scrape output (and therefore the UI catalog) from process start, before
// any real traffic has been served. This is the standard Prometheus pattern
// for closed-set labels — without it, a CounterVec with no observations is
// omitted from /metrics entirely, leaving graphs blank.
//
// Labels with open-ended values (HTTP status codes, endpoint names, error
// kinds) cannot be enumerated up-front; those metrics will appear only after
// their first observation. That trade-off is fine for visibility.
func Init() {
	// TokenMintsTotal {result}
	for _, r := range []string{"success", "denied_license", "denied_scope", "unauthorized"} {
		TokenMintsTotal.WithLabelValues(r).Add(0)
	}
	// BlobRequestsTotal {outcome}
	for _, o := range []string{"redirect", "proxied", "error"} {
		BlobRequestsTotal.WithLabelValues(o).Add(0)
	}
	// LicenseCheckFailuresTotal {reason}
	for _, r := range []string{"expired", "revoked", "parse_error", "sig_invalid", "missing"} {
		LicenseCheckFailuresTotal.WithLabelValues(r).Add(0)
	}
	// AdminLoginsTotal {method, result} — known login methods.
	for _, m := range []string{"password", "static", "static-db", "oidc"} {
		for _, r := range []string{"success", "denied"} {
			AdminLoginsTotal.WithLabelValues(m, r).Add(0)
		}
	}
	// ManifestRequestsTotal {method, result}
	for _, m := range []string{"GET", "HEAD"} {
		for _, r := range []string{"success", "unauthorized", "not_found", "error"} {
			ManifestRequestsTotal.WithLabelValues(m, r).Add(0)
		}
	}
	// GitHubAPIRequestsTotal {result}
	for _, r := range []string{"success", "error", "rate_limited"} {
		GitHubAPIRequestsTotal.WithLabelValues(r).Add(0)
	}
	// GitLabAPIRequestsTotal {result}
	for _, r := range []string{"ok", "upstream_error"} {
		GitLabAPIRequestsTotal.WithLabelValues(r).Add(0)
	}
	// DownloadsTotal {source, outcome}
	for _, s := range []string{"github", "registry"} {
		for _, o := range []string{"success", "error", "unauthorized"} {
			DownloadsTotal.WithLabelValues(s, o).Add(0)
		}
	}
	// Touch the histogram so it shows up too.
	TokenMintLatency.Observe(0)
}
