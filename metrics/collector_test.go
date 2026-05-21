package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector picks up a CounterVec once a label combo has been touched —
// either by real traffic or by Init()-style pre-initialization. Without that
// touch, Prometheus omits the family from Gather() entirely and the UI gets
// an empty catalog (which is the bug that motivated Init()).
func TestCollectorPicksUpCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "ag_test",
		Name:      "hits_total",
		Help:      "test counter",
	}, []string{"result"})
	reg.MustRegister(c)

	// Mirror what metrics.Init() does — pre-touch with .Add(0).
	c.WithLabelValues("success").Add(0)
	c.WithLabelValues("denied").Add(0)

	col := NewCollector(reg, time.Millisecond, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go col.Run(ctx)

	time.Sleep(20 * time.Millisecond)
	cat := col.Catalog()
	foundName := false
	for _, m := range cat {
		if m.Name == "ag_test_hits_total" {
			foundName = true
			if m.Kind != KindCounter {
				t.Fatalf("expected counter kind, got %s", m.Kind)
			}
		}
	}
	if !foundName {
		t.Fatalf("counter not in catalog after warmup; got %v", cat)
	}

	c.WithLabelValues("success").Inc()
	c.WithLabelValues("success").Inc()
	time.Sleep(30 * time.Millisecond)

	q := col.Query("ag_test_hits_total", time.Time{})
	if len(q.Series) == 0 {
		t.Fatalf("expected at least one series after observations, got 0")
	}
	var totalPoints int
	for _, s := range q.Series {
		totalPoints += len(s.Points)
	}
	if totalPoints == 0 {
		t.Fatalf("expected datapoints after observations, got 0")
	}
}

// Histogram quantile interpolation gives sane values for a known distribution.
func TestHistogramQuantile(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "ag_test",
		Name:      "latency_seconds",
		Buckets:   []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	})
	reg.MustRegister(h)
	for i := 0; i < 100; i++ {
		h.Observe(0.03)
	}

	col := NewCollector(reg, time.Millisecond, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go col.Run(ctx)
	time.Sleep(20 * time.Millisecond)

	q := col.Query("ag_test_latency_seconds", time.Time{})
	var sawP95 bool
	for _, s := range q.Series {
		if s.Sub == "p95" && len(s.Points) > 0 {
			sawP95 = true
			v := s.Points[len(s.Points)-1].Value
			if v <= 0 || v > 0.05 {
				t.Fatalf("p95 outside expected range, got %f", v)
			}
		}
	}
	if !sawP95 {
		t.Fatalf("expected a p95 series; got %+v", q.Series)
	}
}
