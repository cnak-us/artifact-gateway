package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TestInitPopulatesCatalog locks in the contract that metrics.Init() makes
// every closed-set artifact_gateway_* metric show up in the catalog before
// any real traffic has hit the gateway. Without Init(), a CounterVec with no
// observed labels is omitted from Gather() entirely and the admin UI is
// blank on a freshly started process.
func TestInitPopulatesCatalog(t *testing.T) {
	Init()

	col := NewCollector(prometheus.DefaultGatherer, time.Millisecond, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go col.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	want := []string{
		"artifact_gateway_token_mints_total",
		"artifact_gateway_blob_requests_total",
		"artifact_gateway_license_check_failures_total",
		"artifact_gateway_admin_logins_total",
		"artifact_gateway_manifest_requests_total",
		"artifact_gateway_github_api_requests_total",
		"artifact_gateway_downloads_total",
		"artifact_gateway_token_mint_latency_seconds",
	}
	have := map[string]bool{}
	for _, m := range col.Catalog() {
		have[m.Name] = true
	}
	for _, n := range want {
		if !have[n] {
			t.Errorf("metric %q missing from catalog after Init()", n)
		}
	}
}
