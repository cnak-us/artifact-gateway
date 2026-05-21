package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/metrics"
)

const (
	defaultGitHubAPIBase = "https://api.github.com"
	githubAPIVersion     = "2022-11-28"
)

// GitHubReleasesClient is a thin wrapper around the GitHub Releases REST API.
// It deliberately avoids any third-party SDK: the surface we need is small
// (list/get release, fetch asset redirect) and stays portable.
//
// The asset-download flow relies on the server replying with a 302 to a
// short-lived signed CDN URL — that redirect is what we hand back to the
// caller so that no PAT ever leaves the gateway.
type GitHubReleasesClient struct {
	Client  *http.Client
	APIBase string // default https://api.github.com
}

// ReleaseAsset is the subset of GitHub's release-asset payload we expose.
type ReleaseAsset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// Release mirrors github.com/<owner>/<repo>/releases/<id> just enough for the
// downloads UI.
type Release struct {
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	Draft       bool           `json:"draft"`
	Prerelease  bool           `json:"prerelease"`
	PublishedAt string         `json:"published_at"`
	Assets      []ReleaseAsset `json:"assets"`
	HTMLURL     string         `json:"html_url"`
}

// RateLimitError is returned when GitHub reports the request quota is exhausted.
// ResetUnix is the X-RateLimit-Reset epoch from the response; callers should
// translate it to a Retry-After delta.
type RateLimitError struct {
	ResetUnix int64
	Status    int
	Message   string
}

func (e *RateLimitError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github rate limit: %s (reset at %d)", e.Message, e.ResetUnix)
	}
	return fmt.Sprintf("github rate limit (reset at %d)", e.ResetUnix)
}

// IsRateLimit reports whether err is a *RateLimitError.
func IsRateLimit(err error) bool {
	var rl *RateLimitError
	return errors.As(err, &rl)
}

// httpError is returned for non-2xx, non-rate-limit responses.
type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("github http %d: %s", e.Status, e.Body)
}

func (g *GitHubReleasesClient) base() string {
	if g.APIBase != "" {
		return strings.TrimRight(g.APIBase, "/")
	}
	return defaultGitHubAPIBase
}

func (g *GitHubReleasesClient) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// recordRateLimit updates the rate-limit gauge from the response headers and
// returns a *RateLimitError if remaining hit zero with a 403/429.
func recordRateLimit(resp *http.Response) error {
	if resp == nil {
		return nil
	}
	if rem := resp.Header.Get("X-RateLimit-Remaining"); rem != "" {
		if n, err := strconv.ParseFloat(rem, 64); err == nil {
			metrics.GitHubAPIRateLimitRemaining.Set(n)
		}
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	if resp.Header.Get("X-RateLimit-Remaining") != "0" &&
		!strings.Contains(strings.ToLower(resp.Header.Get("X-Github-Media-Type")), "rate") {
		// 403 not due to rate limiting (e.g. auth failure) — fall through.
		// But still flag if Retry-After is present, which GitHub uses for
		// secondary limits.
		if resp.Header.Get("Retry-After") == "" {
			return nil
		}
	}
	reset, _ := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if reset == 0 {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.ParseInt(ra, 10, 64); err == nil {
				reset = time.Now().Unix() + secs
			}
		}
	}
	return &RateLimitError{ResetUnix: reset, Status: resp.StatusCode, Message: "rate limit exceeded"}
}

// newRequest builds an authenticated request with the standard GitHub headers.
func (g *GitHubReleasesClient) newRequest(ctx context.Context, method, url, pat, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	if pat != "" {
		req.Header.Set("Authorization", "Bearer "+pat)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "artifact-gateway/1.0")
	}
	return req, nil
}

// ListReleases returns up to `limit` non-draft releases sorted newest-first.
// limit <= 0 defaults to 30 (GitHub's per_page default).
func (g *GitHubReleasesClient) ListReleases(ctx context.Context, repo, pat string, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", g.base(), repo, limit)
	req, err := g.newRequest(ctx, http.MethodGet, url, pat, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, err
	}
	defer resp.Body.Close()

	if rlErr := recordRateLimit(resp); rlErr != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("rate_limited").Inc()
		return nil, rlErr
	}
	if resp.StatusCode >= 400 {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &httpError{Status: resp.StatusCode, Body: string(body)}
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	metrics.GitHubAPIRequestsTotal.WithLabelValues("ok").Inc()

	// Filter out drafts; the spec says "non-draft only".
	out := releases[:0]
	for _, r := range releases {
		if r.Draft {
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// GetRelease returns a single release. tag may be "latest" — in that case the
// /releases/latest endpoint is used (which skips drafts and pre-releases on
// the server side).
func (g *GitHubReleasesClient) GetRelease(ctx context.Context, repo, tag, pat string) (*Release, error) {
	var url string
	if tag == "" || tag == "latest" {
		url = fmt.Sprintf("%s/repos/%s/releases/latest", g.base(), repo)
	} else {
		url = fmt.Sprintf("%s/repos/%s/releases/tags/%s", g.base(), repo, tag)
	}
	req, err := g.newRequest(ctx, http.MethodGet, url, pat, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, err
	}
	defer resp.Body.Close()

	if rlErr := recordRateLimit(resp); rlErr != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("rate_limited").Inc()
		return nil, rlErr
	}
	if resp.StatusCode >= 400 {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &httpError{Status: resp.StatusCode, Body: string(body)}
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, fmt.Errorf("decode release: %w", err)
	}
	metrics.GitHubAPIRequestsTotal.WithLabelValues("ok").Inc()
	return &rel, nil
}

// AssetDownloadURL performs the asset-metadata fetch with
// Accept: application/octet-stream and returns the redirect Location to the
// signed CDN URL. Does NOT follow the redirect — that's the whole point of
// the zero-egress design.
//
// On success returns (locationURL, 302, nil). On non-302 returns
// ("", status, error) with the error describing the upstream condition.
func (g *GitHubReleasesClient) AssetDownloadURL(ctx context.Context, repo string, assetID int64, pat string) (string, int, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/assets/%d", g.base(), repo, assetID)
	req, err := g.newRequest(ctx, http.MethodGet, url, pat, "application/octet-stream")
	if err != nil {
		return "", 0, err
	}

	// We must NOT follow the redirect — the signed URL is what we hand back.
	// Use a per-request client that overrides CheckRedirect so we don't have
	// to mutate the shared client.
	base := g.client()
	c := &http.Client{
		Transport: base.Transport,
		Timeout:   base.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return "", 0, err
	}
	defer resp.Body.Close()

	if rlErr := recordRateLimit(resp); rlErr != nil {
		metrics.GitHubAPIRequestsTotal.WithLabelValues("rate_limited").Inc()
		return "", resp.StatusCode, rlErr
	}
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect {
		loc := resp.Header.Get("Location")
		if loc == "" {
			metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
			return "", resp.StatusCode, fmt.Errorf("github asset %d: redirect with empty Location", assetID)
		}
		metrics.GitHubAPIRequestsTotal.WithLabelValues("ok").Inc()
		return loc, http.StatusFound, nil
	}
	if resp.StatusCode == http.StatusOK {
		// Some setups (notably GHES with non-redirecting storage) stream the
		// bytes directly. We don't currently proxy that path; surface it as
		// an error so the caller can decide.
		metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return "", resp.StatusCode, fmt.Errorf("github asset %d: 200 OK without Location (gateway does not proxy bytes)", assetID)
	}
	metrics.GitHubAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return "", resp.StatusCode, &httpError{Status: resp.StatusCode, Body: string(body)}
}
