package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cnak-us/artifact-gateway/metrics"
)

// listMetricsCatalog returns the set of metrics observed by the in-process
// collector, plus retention info so the UI can size its x-axis.
func listMetricsCatalog(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Metrics == nil {
			writeJSON(w, http.StatusOK, catalogResp{Metrics: []metrics.MetricMeta{}})
			return
		}
		writeJSON(w, http.StatusOK, catalogResp{
			Metrics: d.Metrics.Catalog(),
		})
	}
}

type catalogResp struct {
	Metrics []metrics.MetricMeta `json:"metrics"`
}

// getMetricsSeries returns a single metric's time series. The metric name is
// passed as ?name=foo; an optional ?since_secs=300 limits results to the last
// N seconds (default: all retained data).
func getMetricsSeries(d AdminDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Metrics == nil {
			writeJSONErr(w, http.StatusServiceUnavailable, "metrics collector not configured")
			return
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			writeJSONErr(w, http.StatusBadRequest, "name query parameter required")
			return
		}
		var since time.Time
		if v := r.URL.Query().Get("since_secs"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				since = time.Now().Add(-time.Duration(n) * time.Second)
			}
		}
		writeJSON(w, http.StatusOK, d.Metrics.Query(name, since))
	}
}
