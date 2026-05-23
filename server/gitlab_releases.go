package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cnak-us/artifact-gateway/metrics"
)

// GitLabReleasesClient is the GitLab counterpart to GitHubReleasesClient.
// GitLab Releases live under /api/v4/projects/:id/releases on whatever
// GitLab host the credential's BaseURL points at — SaaS gitlab.com or any
// self-hosted instance.
//
// Key differences from GitHub:
//   - Auth uses the PRIVATE-TOKEN header (or Authorization: Bearer for
//     OAuth-style tokens — we use PRIVATE-TOKEN, which works for PATs,
//     project/group access tokens, and Deploy Tokens alike).
//   - There is no `/releases/latest` endpoint; "latest" is emulated by
//     fetching the first non-upcoming release from the descending list.
//   - Asset "download URLs" are operator-chosen Release Links — they can
//     point at the GitLab Package Registry, an external CDN, or anywhere.
//     We hand the URL back as-is; the downloads code may need to proxy bytes
//     when the URL points back into the same GitLab instance (a follow-up).
type GitLabReleasesClient struct {
	Client *http.Client
}

func (g *GitLabReleasesClient) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// gitlabAPIBase normalizes a credential BaseURL into `<scheme>://<host>/api/v4`.
// If the operator already pasted in `/api/v4`, leave it alone.
func gitlabAPIBase(baseURL string) string {
	b := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(b, "/api/v4") {
		return b
	}
	return b + "/api/v4"
}

// projectPathID URL-escapes a `group/subgroup/project` path so it can stand
// in for the numeric :id in REST URLs. GitLab accepts both numeric IDs and
// URL-encoded full paths.
func projectPathID(repo string) string {
	return url.PathEscape(strings.Trim(repo, "/"))
}

// gitlabRelease is the JSON payload GitLab returns from /releases.
// Mapped onto Release at decode time so the rest of the downloads code path
// doesn't have to care.
type gitlabRelease struct {
	Name            string `json:"name"`
	TagName         string `json:"tag_name"`
	ReleasedAt      string `json:"released_at"`
	UpcomingRelease bool   `json:"upcoming_release"`
	Links           struct {
		Self string `json:"self"`
	} `json:"_links"`
	Assets struct {
		Count int                 `json:"count"`
		Links []gitlabReleaseLink `json:"links"`
	} `json:"assets"`
}

type gitlabReleaseLink struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	DirectAssetURL  string `json:"direct_asset_url"`
	LinkType        string `json:"link_type"`
}

// toCommon flattens a gitlabRelease into the cross-vendor Release shape used
// by downloads.go. Release Links become assets; Size/ContentType are unknown
// (GitLab doesn't surface them) — we leave them zeroed.
func (r *gitlabRelease) toCommon() Release {
	out := Release{
		TagName:     r.TagName,
		Name:        r.Name,
		Prerelease:  r.UpcomingRelease,
		PublishedAt: r.ReleasedAt,
		HTMLURL:     r.Links.Self,
		Assets:      make([]ReleaseAsset, 0, len(r.Assets.Links)),
	}
	for _, l := range r.Assets.Links {
		out.Assets = append(out.Assets, ReleaseAsset{
			ID:   l.ID,
			Name: l.Name,
		})
	}
	return out
}

// newRequest builds an authenticated GitLab API request.
func (g *GitLabReleasesClient) newRequest(ctx context.Context, method, url, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		// PRIVATE-TOKEN works for PATs, Project/Group Access Tokens, and
		// Deploy Tokens alike. Avoids the two-flavor Bearer/PRIVATE-TOKEN
		// branching some clients do.
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	req.Header.Set("Accept", "application/json")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "artifact-gateway/1.0")
	}
	return req, nil
}

// ListReleases returns up to `limit` non-upcoming releases sorted newest-first.
// baseURL must include the scheme + host (e.g. https://gitlab.example.com).
func (g *GitLabReleasesClient) ListReleases(ctx context.Context, baseURL, repo, token string, limit int) ([]Release, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	api := fmt.Sprintf("%s/projects/%s/releases?per_page=%d&order_by=released_at&sort=desc",
		gitlabAPIBase(baseURL), projectPathID(repo), limit)
	req, err := g.newRequest(ctx, http.MethodGet, api, token)
	if err != nil {
		return nil, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &httpError{Status: resp.StatusCode, Body: string(body)}
	}
	var rels []gitlabRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, fmt.Errorf("decode gitlab releases: %w", err)
	}
	metrics.GitLabAPIRequestsTotal.WithLabelValues("ok").Inc()

	out := make([]Release, 0, len(rels))
	for i := range rels {
		if rels[i].UpcomingRelease {
			continue
		}
		out = append(out, rels[i].toCommon())
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// GetRelease fetches one release by tag, or the latest (descending-list head)
// when tag is "" or "latest". GitLab doesn't have a /releases/latest endpoint.
func (g *GitLabReleasesClient) GetRelease(ctx context.Context, baseURL, repo, tag, token string) (*Release, error) {
	if tag == "" || tag == "latest" {
		rels, err := g.ListReleases(ctx, baseURL, repo, token, 1)
		if err != nil {
			return nil, err
		}
		if len(rels) == 0 {
			return nil, &httpError{Status: http.StatusNotFound, Body: "no releases"}
		}
		first := rels[0]
		return &first, nil
	}
	api := fmt.Sprintf("%s/projects/%s/releases/%s",
		gitlabAPIBase(baseURL), projectPathID(repo), url.PathEscape(tag))
	req, err := g.newRequest(ctx, http.MethodGet, api, token)
	if err != nil {
		return nil, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &httpError{Status: resp.StatusCode, Body: string(body)}
	}
	var raw gitlabRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return nil, fmt.Errorf("decode gitlab release: %w", err)
	}
	metrics.GitLabAPIRequestsTotal.WithLabelValues("ok").Inc()
	rel := raw.toCommon()
	return &rel, nil
}

// AssetDownloadURL returns the Release Link's direct URL for the given asset
// ID. Unlike GitHub, there's no asset-redirect endpoint — the URL stored on
// the link is what callers download from. When the URL points at the same
// GitLab host as the API base, the caller may need to attach the PAT; this
// client returns the URL verbatim and leaves that decision to the proxy.
func (g *GitLabReleasesClient) AssetDownloadURL(ctx context.Context, baseURL, repo string, assetID int64, token string) (string, int, error) {
	rels, err := g.ListReleases(ctx, baseURL, repo, token, 100)
	if err != nil {
		return "", 0, err
	}
	// GitLab assets don't live under a stable /assets/:id path the way
	// GitHub's do — they're nested under each release. To resolve an asset
	// by ID without a tag, we scan recent releases. Callers that have the
	// tag in hand should use AssetDownloadURLByTag below for an O(1) fetch.
	for _, rel := range rels {
		for i := range rel.Assets {
			if rel.Assets[i].ID == assetID {
				// Re-fetch by tag to pick the link's direct URL (the flat
				// asset list above doesn't carry URL fields).
				return g.assetURLForRelease(ctx, baseURL, repo, rel.TagName, assetID, token)
			}
		}
	}
	return "", http.StatusNotFound, errors.New("gitlab asset not found")
}

// assetURLForRelease pulls a single release and returns the link URL for the
// requested asset ID.
func (g *GitLabReleasesClient) assetURLForRelease(ctx context.Context, baseURL, repo, tag string, assetID int64, token string) (string, int, error) {
	api := fmt.Sprintf("%s/projects/%s/releases/%s",
		gitlabAPIBase(baseURL), projectPathID(repo), url.PathEscape(tag))
	req, err := g.newRequest(ctx, http.MethodGet, api, token)
	if err != nil {
		return "", 0, err
	}
	resp, err := g.client().Do(req)
	if err != nil {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", resp.StatusCode, &httpError{Status: resp.StatusCode, Body: string(body)}
	}
	var raw gitlabRelease
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		metrics.GitLabAPIRequestsTotal.WithLabelValues("upstream_error").Inc()
		return "", 0, fmt.Errorf("decode gitlab release: %w", err)
	}
	metrics.GitLabAPIRequestsTotal.WithLabelValues("ok").Inc()
	for _, l := range raw.Assets.Links {
		if l.ID == assetID {
			u := l.DirectAssetURL
			if u == "" {
				u = l.URL
			}
			if u == "" {
				return "", http.StatusNotFound, errors.New("gitlab link has no url")
			}
			return u, http.StatusFound, nil
		}
	}
	return "", http.StatusNotFound, errors.New("gitlab asset not found in release")
}
