package metrics

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSeriesJSONContract pins the JSON field names the admin UI reads. A
// silent rename here (e.g. "v" instead of "value") will produce blank charts
// because the UI parser reads `p.value` directly.
func TestSeriesJSONContract(t *testing.T) {
	resp := SeriesResponse{
		Name: "x",
		Kind: KindCounter,
		Series: []SeriesEntry{{
			Labels: map[string]string{"a": "b"},
			Sub:    "p95",
			Points: []SeriesPoint{{TS: 123, Value: 4.5}},
		}},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, frag := range []string{
		`"name":"x"`,
		`"kind":"counter"`,
		`"series":[`,
		`"labels":{"a":"b"}`,
		`"sub":"p95"`,
		`"points":[`,
		`"ts":123`,
		`"value":4.5`,
	} {
		if !strings.Contains(got, frag) {
			t.Errorf("expected JSON to contain %q; got %s", frag, got)
		}
	}
}
