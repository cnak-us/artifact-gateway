package metrics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
)

// Kind classifies a series for the UI.
type Kind string

const (
	KindCounter   Kind = "counter"
	KindGauge     Kind = "gauge"
	KindHistogram Kind = "histogram" // count + sum + quantile sub-series
	KindSummary   Kind = "summary"
)

// SeriesKey identifies a unique label set for a metric.
type SeriesKey struct {
	Metric string `json:"metric"`
	Labels string `json:"labels"` // canonical "k1=v1,k2=v2"; empty for unlabeled
	Sub    string `json:"sub"`    // "", "count", "sum", "p50", "p95", "p99"
}

// String renders a SeriesKey for use as a map key.
func (k SeriesKey) String() string {
	if k.Sub == "" {
		if k.Labels == "" {
			return k.Metric
		}
		return k.Metric + "{" + k.Labels + "}"
	}
	if k.Labels == "" {
		return k.Metric + ":" + k.Sub
	}
	return k.Metric + "{" + k.Labels + "}:" + k.Sub
}

// MetricMeta describes a metric in the catalog response.
type MetricMeta struct {
	Name   string   `json:"name"`
	Help   string   `json:"help"`
	Kind   Kind     `json:"kind"`
	Labels []string `json:"labels,omitempty"`
}

// Sample is one (key, value) pair at a snapshot timestamp.
type Sample struct {
	Key   SeriesKey
	Value float64
}

// Snapshot is the full set of series samples at one instant.
type Snapshot struct {
	TS      time.Time
	Samples []Sample
}

// Collector periodically gathers a Prometheus registry and keeps a ring
// buffer of recent snapshots so the UI can render time series without an
// external Prometheus server.
type Collector struct {
	gatherer prometheus.Gatherer
	interval time.Duration

	mu       sync.RWMutex
	ring     []Snapshot
	head     int // index of next slot to write
	full     bool
	metaByName map[string]MetricMeta
}

// NewCollector builds a collector backed by the given gatherer.
// capacity is the number of snapshots to retain (e.g. 720 = 1h at 5s).
func NewCollector(g prometheus.Gatherer, interval time.Duration, capacity int) *Collector {
	if g == nil {
		g = prometheus.DefaultGatherer
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if capacity <= 0 {
		capacity = 720
	}
	return &Collector{
		gatherer:   g,
		interval:   interval,
		ring:       make([]Snapshot, capacity),
		metaByName: map[string]MetricMeta{},
	}
}

// Run gathers a snapshot immediately and then on every tick until ctx is done.
func (c *Collector) Run(ctx context.Context) {
	c.gatherOnce()
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.gatherOnce()
		}
	}
}

func (c *Collector) gatherOnce() {
	mfs, err := c.gatherer.Gather()
	if err != nil {
		return
	}
	now := time.Now()
	samples := make([]Sample, 0, 64)
	meta := map[string]MetricMeta{}

	for _, mf := range mfs {
		name := mf.GetName()
		help := mf.GetHelp()
		kind := dtoTypeToKind(mf.GetType())
		var labels []string
		for _, m := range mf.GetMetric() {
			labelsSig, lkeys := canonicalLabels(m.GetLabel())
			if len(labels) == 0 {
				labels = lkeys
			}
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				samples = append(samples, Sample{
					Key:   SeriesKey{Metric: name, Labels: labelsSig},
					Value: m.GetCounter().GetValue(),
				})
			case dto.MetricType_GAUGE:
				samples = append(samples, Sample{
					Key:   SeriesKey{Metric: name, Labels: labelsSig},
					Value: m.GetGauge().GetValue(),
				})
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				count := float64(h.GetSampleCount())
				sum := h.GetSampleSum()
				samples = append(samples,
					Sample{Key: SeriesKey{Metric: name, Labels: labelsSig, Sub: "count"}, Value: count},
					Sample{Key: SeriesKey{Metric: name, Labels: labelsSig, Sub: "sum"}, Value: sum},
				)
				for _, q := range []float64{0.5, 0.95, 0.99} {
					v := histogramQuantile(q, h.GetBucket(), count)
					samples = append(samples, Sample{
						Key:   SeriesKey{Metric: name, Labels: labelsSig, Sub: fmt.Sprintf("p%d", int(q*100))},
						Value: v,
					})
				}
			case dto.MetricType_SUMMARY:
				s := m.GetSummary()
				samples = append(samples,
					Sample{Key: SeriesKey{Metric: name, Labels: labelsSig, Sub: "count"}, Value: float64(s.GetSampleCount())},
					Sample{Key: SeriesKey{Metric: name, Labels: labelsSig, Sub: "sum"}, Value: s.GetSampleSum()},
				)
				for _, q := range s.GetQuantile() {
					samples = append(samples, Sample{
						Key:   SeriesKey{Metric: name, Labels: labelsSig, Sub: fmt.Sprintf("p%d", int(q.GetQuantile()*100))},
						Value: q.GetValue(),
					})
				}
			}
		}
		meta[name] = MetricMeta{Name: name, Help: help, Kind: kind, Labels: labels}
	}

	c.mu.Lock()
	c.ring[c.head] = Snapshot{TS: now, Samples: samples}
	c.head = (c.head + 1) % len(c.ring)
	if c.head == 0 {
		c.full = true
	}
	for k, v := range meta {
		c.metaByName[k] = v
	}
	c.mu.Unlock()
}

// Catalog returns the list of metrics seen by the collector, sorted by name.
func (c *Collector) Catalog() []MetricMeta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]MetricMeta, 0, len(c.metaByName))
	for _, m := range c.metaByName {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SeriesPoint is one (timestamp, value) pair in JSON-friendly form.
type SeriesPoint struct {
	TS    int64   `json:"ts"` // unix seconds
	Value float64 `json:"value"`
}

// SeriesResponse bundles all series for one metric name.
type SeriesResponse struct {
	Name   string                    `json:"name"`
	Help   string                    `json:"help"`
	Kind   Kind                      `json:"kind"`
	Series []SeriesEntry             `json:"series"`
}

// SeriesEntry is one labeled time series within a metric.
type SeriesEntry struct {
	Labels map[string]string `json:"labels"`
	Sub    string            `json:"sub,omitempty"`
	Points []SeriesPoint     `json:"points"`
}

// Query returns the in-memory series for one metric name since the given
// timestamp (or all retained data when since is zero).
func (c *Collector) Query(name string, since time.Time) SeriesResponse {
	c.mu.RLock()
	defer c.mu.RUnlock()

	meta, ok := c.metaByName[name]
	resp := SeriesResponse{Name: name}
	if ok {
		resp.Help = meta.Help
		resp.Kind = meta.Kind
	}

	type entryKey struct {
		labels string
		sub    string
	}
	entries := map[entryKey]*SeriesEntry{}

	walkRing(c.ring, c.head, c.full, func(snap Snapshot) {
		if !since.IsZero() && snap.TS.Before(since) {
			return
		}
		ts := snap.TS.Unix()
		for _, s := range snap.Samples {
			if s.Key.Metric != name {
				continue
			}
			k := entryKey{labels: s.Key.Labels, sub: s.Key.Sub}
			e, ok := entries[k]
			if !ok {
				e = &SeriesEntry{
					Labels: parseLabels(s.Key.Labels),
					Sub:    s.Key.Sub,
				}
				entries[k] = e
			}
			e.Points = append(e.Points, SeriesPoint{TS: ts, Value: s.Value})
		}
	})

	resp.Series = make([]SeriesEntry, 0, len(entries))
	for _, e := range entries {
		resp.Series = append(resp.Series, *e)
	}
	sort.Slice(resp.Series, func(i, j int) bool {
		if resp.Series[i].Sub != resp.Series[j].Sub {
			return resp.Series[i].Sub < resp.Series[j].Sub
		}
		return labelStr(resp.Series[i].Labels) < labelStr(resp.Series[j].Labels)
	})
	return resp
}

// --- helpers ----------------------------------------------------------------

func dtoTypeToKind(t dto.MetricType) Kind {
	switch t {
	case dto.MetricType_COUNTER:
		return KindCounter
	case dto.MetricType_GAUGE:
		return KindGauge
	case dto.MetricType_HISTOGRAM:
		return KindHistogram
	case dto.MetricType_SUMMARY:
		return KindSummary
	}
	return KindGauge
}

func canonicalLabels(pairs []*dto.LabelPair) (string, []string) {
	if len(pairs) == 0 {
		return "", nil
	}
	kvs := make([]string, 0, len(pairs))
	keys := make([]string, 0, len(pairs))
	for _, p := range pairs {
		kvs = append(kvs, p.GetName()+"="+p.GetValue())
		keys = append(keys, p.GetName())
	}
	sort.Strings(kvs)
	sort.Strings(keys)
	return strings.Join(kvs, ","), keys
}

func parseLabels(sig string) map[string]string {
	out := map[string]string{}
	if sig == "" {
		return out
	}
	for _, kv := range strings.Split(sig, ",") {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		out[kv[:eq]] = kv[eq+1:]
	}
	return out
}

func labelStr(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// histogramQuantile approximates the q-quantile (0..1) from cumulative buckets.
// Mirrors Prometheus's linear interpolation within the matched bucket.
func histogramQuantile(q float64, buckets []*dto.Bucket, count float64) float64 {
	if count <= 0 || len(buckets) == 0 {
		return math.NaN()
	}
	rank := q * count
	var prevCount float64
	var prevBound float64
	for _, b := range buckets {
		c := float64(b.GetCumulativeCount())
		bound := b.GetUpperBound()
		if rank <= c {
			if math.IsInf(bound, +1) {
				return prevBound
			}
			if c == prevCount {
				return bound
			}
			return prevBound + (bound-prevBound)*(rank-prevCount)/(c-prevCount)
		}
		prevCount = c
		prevBound = bound
	}
	return prevBound
}

// walkRing iterates oldest→newest over the ring buffer.
func walkRing(ring []Snapshot, head int, full bool, fn func(Snapshot)) {
	n := len(ring)
	start := 0
	count := head
	if full {
		start = head
		count = n
	}
	for i := 0; i < count; i++ {
		idx := (start + i) % n
		s := ring[idx]
		if s.TS.IsZero() {
			continue
		}
		fn(s)
	}
}
