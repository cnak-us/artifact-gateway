package server

import "testing"

func TestAssetAllowed(t *testing.T) {
	cases := []struct {
		name    string
		asset   string
		pattern string
		want    bool
	}{
		{"empty pattern allows", "cnak-darwin-amd64.tar.gz", "", true},
		{"empty pattern allows other", "anything.zip", "", true},
		{"whitespace-only pattern allows", "cnak-darwin-amd64.tar.gz", "   ", true},
		{"star matches all", "cnak-darwin-amd64.tar.gz", "*", true},
		{"star matches checksums", "checksums.txt", "*", true},

		{"literal match", "checksums.txt", "checksums.txt", true},
		{"literal reject", "cnak-darwin-amd64.tar.gz", "checksums.txt", false},

		{"glob match darwin", "cnak-darwin-amd64.tar.gz", "cnak-darwin-*", true},
		{"glob match darwin arm", "cnak-darwin-arm64.tar.gz", "cnak-darwin-*", true},
		{"glob rejects linux", "cnak-linux-amd64.tar.gz", "cnak-darwin-*", false},

		{"multi pattern darwin", "cnak-darwin-amd64.tar.gz", "cnak-darwin-*,checksums.txt", true},
		{"multi pattern checksums", "checksums.txt", "cnak-darwin-*,checksums.txt", true},
		{"multi pattern reject linux", "cnak-linux-amd64.tar.gz", "cnak-darwin-*,checksums.txt", false},
		{"multi pattern reject windows", "cnak-windows-amd64.zip", "cnak-darwin-*,checksums.txt", false},

		{"leading comma", "checksums.txt", ",checksums.txt", true},
		{"trailing comma", "checksums.txt", "checksums.txt,", true},
		{"whitespace around segments", "checksums.txt", "  cnak-darwin-* , checksums.txt  ", true},
		{"only commas does not match", "checksums.txt", ",,,", false},

		{"invalid glob bad bracket", "checksums.txt", "[bad", false},
		{"invalid glob with valid fallback", "checksums.txt", "[bad,checksums.txt", true},

		{"question mark glob match", "v1.tar.gz", "v?.tar.gz", true},
		{"question mark glob reject", "v10.tar.gz", "v?.tar.gz", false},

		{"char class match", "v1.tar.gz", "v[0-9].tar.gz", true},
		{"char class reject", "va.tar.gz", "v[0-9].tar.gz", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assetAllowed(tc.asset, tc.pattern)
			if got != tc.want {
				t.Errorf("assetAllowed(%q, %q) = %v, want %v", tc.asset, tc.pattern, got, tc.want)
			}
		})
	}
}
