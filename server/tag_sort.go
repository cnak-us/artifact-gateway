package server

import (
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// sortTagsSemverDesc returns tags ordered newest-first by semver, with
// non-semver tags (e.g. "latest", "nightly") appended at the end in reverse
// lexicographic order. Pre-releases sort below their release counterpart
// (1.0.0 > 1.0.0-rc1) per the semver spec. Masterminds/semver accepts both
// `1.2.3` and `v1.2.3`, so callers can pass raw OCI tags directly. The
// input slice is not mutated.
func sortTagsSemverDesc(tags []string) []string {
	type parsed struct {
		raw string
		ver *semver.Version
	}
	semverTags := make([]parsed, 0, len(tags))
	other := make([]string, 0)
	for _, t := range tags {
		v, err := semver.NewVersion(strings.TrimSpace(t))
		if err != nil {
			other = append(other, t)
			continue
		}
		semverTags = append(semverTags, parsed{raw: t, ver: v})
	}
	sort.SliceStable(semverTags, func(i, j int) bool {
		return semverTags[i].ver.GreaterThan(semverTags[j].ver)
	})
	sort.SliceStable(other, func(i, j int) bool { return other[i] > other[j] })

	out := make([]string, 0, len(tags))
	for _, p := range semverTags {
		out = append(out, p.raw)
	}
	out = append(out, other...)
	return out
}
