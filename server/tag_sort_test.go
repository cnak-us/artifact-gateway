package server

import (
	"reflect"
	"testing"
)

func TestSortTagsSemverDesc(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "pure semver, mixed lengths",
			in:   []string{"0.2.0-beta", "latest", "0.2.1-beta", "0.2.10", "0.2.9", "0.3.0"},
			want: []string{"0.3.0", "0.2.10", "0.2.9", "0.2.1-beta", "0.2.0-beta", "latest"},
		},
		{
			name: "v-prefixed and bare mixed",
			in:   []string{"v1.0.0", "1.0.0-rc1", "v2.0.0"},
			want: []string{"v2.0.0", "v1.0.0", "1.0.0-rc1"},
		},
		{
			name: "all non-semver falls back to reverse lex",
			in:   []string{"latest", "nightly", "edge"},
			want: []string{"nightly", "latest", "edge"},
		},
		{
			name: "empty",
			in:   nil,
			want: []string{},
		},
		{
			name: "real ghcr response",
			in: []string{
				"0.2.0-beta", "latest", "0.2.1-beta", "0.2.2-beta", "0.2.3-beta",
				"0.2.4", "0.2.5", "0.2.6", "0.2.7", "0.2.9", "0.2.10", "0.3.0",
			},
			want: []string{
				"0.3.0", "0.2.10", "0.2.9", "0.2.7", "0.2.6", "0.2.5", "0.2.4",
				"0.2.3-beta", "0.2.2-beta", "0.2.1-beta", "0.2.0-beta", "latest",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sortTagsSemverDesc(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
