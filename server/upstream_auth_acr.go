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
)

// acrIssuerSecret is the JSON shape stored in IssuerSecretEnc for
// kind=acr-aad. Service-principal credentials only — managed identity
// requires running inside Azure and is out of scope for v1.
type acrIssuerSecret struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// acrIssuerConfig holds non-secret per-credential config: AAD tenant and the
// target ACR hostname. Registry host travels in BaseURL too; this is what
// the AAD/ACR exchange endpoints need.
type acrIssuerConfig struct {
	TenantID string `json:"tenantId"`
	// Registry is the ACR host, e.g. "myreg.azurecr.io" (no scheme). When
	// empty we derive it from the credential's BaseURL.
	Registry string `json:"registry"`
}

// acrMinter exchanges an AAD client-credentials access token for an ACR
// refresh token, then for a registry-wide ACR access token. The access
// token lasts ~5 minutes; the refresh token lasts ~3 hours. We cache the
// 5-minute access token (the parent IssuerMintAuthenticator handles
// refresh-before-expiry).
//
// All three calls are vanilla HTTP — no Azure SDK dependency.
type acrMinter struct {
	HTTPClient *http.Client
}

func (m acrMinter) client() *http.Client {
	if m.HTTPClient != nil {
		return m.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (m acrMinter) Mint(ctx context.Context, secret []byte, configJSON []byte) (mintedToken, error) {
	var s acrIssuerSecret
	if err := json.Unmarshal(secret, &s); err != nil {
		return mintedToken{}, fmt.Errorf("decode acr issuer secret: %w", err)
	}
	var c acrIssuerConfig
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &c); err != nil {
			return mintedToken{}, fmt.Errorf("decode acr issuer config: %w", err)
		}
	}
	if s.ClientID == "" || s.ClientSecret == "" {
		return mintedToken{}, errors.New("acr secret missing clientId/clientSecret")
	}
	if c.TenantID == "" {
		return mintedToken{}, errors.New("acr config missing tenantId")
	}
	if c.Registry == "" {
		return mintedToken{}, errors.New("acr config missing registry hostname")
	}
	registry := strings.TrimPrefix(strings.TrimPrefix(c.Registry, "https://"), "http://")

	aadToken, err := m.aadAccessToken(ctx, c.TenantID, s.ClientID, s.ClientSecret)
	if err != nil {
		return mintedToken{}, err
	}
	refresh, err := m.acrRefreshToken(ctx, registry, c.TenantID, aadToken)
	if err != nil {
		return mintedToken{}, err
	}
	access, ttl, err := m.acrAccessToken(ctx, registry, refresh)
	if err != nil {
		return mintedToken{}, err
	}
	return mintedToken{
		AuthHeader: "Bearer " + access,
		TTL:        ttl,
	}, nil
}

// aadAccessToken does an OAuth2 client_credentials grant against AAD. Scope
// "https://management.azure.com/.default" is what ACR's exchange endpoint
// validates against.
func (m acrMinter) aadAccessToken(ctx context.Context, tenant, clientID, clientSecret string) (string, error) {
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenant)
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("scope", "https://management.azure.com/.default")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("aad token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("aad token %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("aad returned empty access_token")
	}
	return body.AccessToken, nil
}

// acrRefreshToken exchanges an AAD access token for an ACR-scoped refresh
// token. The exchange endpoint lives on the registry itself.
func (m acrMinter) acrRefreshToken(ctx context.Context, registry, tenant, aadToken string) (string, error) {
	exchangeURL := fmt.Sprintf("https://%s/oauth2/exchange", registry)
	form := url.Values{}
	form.Set("grant_type", "access_token")
	form.Set("service", registry)
	form.Set("tenant", tenant)
	form.Set("access_token", aadToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("acr exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("acr exchange %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.RefreshToken == "" {
		return "", errors.New("acr exchange returned empty refresh_token")
	}
	return body.RefreshToken, nil
}

// acrAccessToken exchanges an ACR refresh token for a short-lived access
// token. We ask for the registry-wide pull scope; per-repo scoping is
// possible too but the IssuerMintAuthenticator's cache is keyed per
// credential, not per repo.
func (m acrMinter) acrAccessToken(ctx context.Context, registry, refresh string) (string, time.Duration, error) {
	tokenURL := fmt.Sprintf("https://%s/oauth2/token", registry)
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("service", registry)
	form.Set("scope", "registry:catalog:*")
	form.Set("refresh_token", refresh)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("acr token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", 0, fmt.Errorf("acr token %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", 0, err
	}
	if body.AccessToken == "" {
		return "", 0, errors.New("acr token returned empty access_token")
	}
	ttl := time.Duration(body.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return body.AccessToken, ttl, nil
}

// defaultIssuerMinters wires the package-default minters for each bucket-C
// Kind. Tests substitute via IssuerMintAuthenticator.RegisterMinter.
func defaultIssuerMinters() map[string]IssuerMinter {
	return map[string]IssuerMinter{
		"ecr":     ecrMinter{},
		"gar":     garMinter{},
		"acr-aad": acrMinter{},
	}
}
