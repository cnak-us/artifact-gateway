package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2/google"
)

// garIssuerConfig is the JSON shape stored in issuer_config for kind=gar.
// BaseURL on the credential row carries the registry hostname
// (e.g. "https://us-docker.pkg.dev"); no additional fields are mandatory.
type garIssuerConfig struct {
	// Scopes overrides the OAuth2 scopes used when minting an access
	// token. Defaults to cloud-platform read-only, which is sufficient
	// for Artifact Registry / legacy GCR pulls.
	Scopes []string `json:"scopes,omitempty"`
}

// garMinter mints an OAuth2 access token from a Google service-account JSON
// key and packages it as Basic `oauth2accesstoken:<token>`. Google's
// Artifact Registry accepts that on `/v2/*`. Access tokens last ~1 hour.
type garMinter struct{}

func (garMinter) Mint(ctx context.Context, secret []byte, configJSON []byte) (mintedToken, error) {
	if len(secret) == 0 {
		return mintedToken{}, errors.New("gar issuer secret is empty")
	}
	var c garIssuerConfig
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &c); err != nil {
			return mintedToken{}, fmt.Errorf("decode gar issuer config: %w", err)
		}
	}
	scopes := c.Scopes
	if len(scopes) == 0 {
		scopes = []string{"https://www.googleapis.com/auth/cloud-platform.read-only"}
	}
	creds, err := google.CredentialsFromJSON(ctx, secret, scopes...)
	if err != nil {
		return mintedToken{}, fmt.Errorf("parse gcp credentials: %w", err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return mintedToken{}, fmt.Errorf("mint gcp access token: %w", err)
	}
	ttl := time.Until(tok.Expiry)
	if ttl <= 0 {
		ttl = 50 * time.Minute
	}
	return mintedToken{
		AuthHeader: EncodeBasic("oauth2accesstoken", tok.AccessToken),
		TTL:        ttl,
	}, nil
}
