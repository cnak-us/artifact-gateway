package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
)

// ecrIssuerSecret is the JSON shape stored in IssuerSecretEnc for kind=ecr.
// Either static keys or a sessionToken (for STS assume-role). Region travels
// in issuer_config alongside; not here.
type ecrIssuerSecret struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
}

// ecrIssuerConfig is the JSON shape stored in issuer_config for kind=ecr.
type ecrIssuerConfig struct {
	Region string `json:"region"`
	// AccountID is optional and only used when probing — the registry
	// hostname `<account>.dkr.ecr.<region>.amazonaws.com` derives from it.
	AccountID string `json:"accountId,omitempty"`
}

// ecrMinter calls ecr:GetAuthorizationToken to mint a 12-hour Basic auth
// credential against `<account>.dkr.ecr.<region>.amazonaws.com`.
type ecrMinter struct{}

func (ecrMinter) Mint(ctx context.Context, secret []byte, configJSON []byte) (mintedToken, error) {
	var s ecrIssuerSecret
	if err := json.Unmarshal(secret, &s); err != nil {
		return mintedToken{}, fmt.Errorf("decode ecr issuer secret: %w", err)
	}
	if s.AccessKeyID == "" || s.SecretAccessKey == "" {
		return mintedToken{}, errors.New("ecr issuer secret missing accessKeyId/secretAccessKey")
	}
	var c ecrIssuerConfig
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &c); err != nil {
			return mintedToken{}, fmt.Errorf("decode ecr issuer config: %w", err)
		}
	}
	if c.Region == "" {
		return mintedToken{}, errors.New("ecr issuer config missing region")
	}

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(c.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s.AccessKeyID, s.SecretAccessKey, s.SessionToken,
		)),
	)
	if err != nil {
		return mintedToken{}, fmt.Errorf("load aws config: %w", err)
	}
	client := ecr.NewFromConfig(awsCfg)
	out, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return mintedToken{}, fmt.Errorf("ecr GetAuthorizationToken: %w", err)
	}
	if len(out.AuthorizationData) == 0 || out.AuthorizationData[0].AuthorizationToken == nil {
		return mintedToken{}, errors.New("ecr returned no authorization data")
	}
	enc := aws.ToString(out.AuthorizationData[0].AuthorizationToken)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return mintedToken{}, fmt.Errorf("decode ecr token: %w", err)
	}
	user, pw, ok := strings.Cut(string(raw), ":")
	if !ok {
		return mintedToken{}, errors.New("ecr token has unexpected shape")
	}
	ttl := 12 * time.Hour
	if out.AuthorizationData[0].ExpiresAt != nil {
		ttl = time.Until(*out.AuthorizationData[0].ExpiresAt)
	}
	return mintedToken{
		AuthHeader: EncodeBasic(user, pw),
		TTL:        ttl,
	}, nil
}

// ecrRegistryHost returns "<account>.dkr.ecr.<region>.amazonaws.com" from
// the stored issuer_config. Used by effectiveHost.
func ecrRegistryHost(configJSON []byte) string {
	var c ecrIssuerConfig
	if json.Unmarshal(configJSON, &c) != nil {
		return ""
	}
	if c.AccountID == "" || c.Region == "" {
		return ""
	}
	return fmt.Sprintf("https://%s.dkr.ecr.%s.amazonaws.com", c.AccountID, c.Region)
}
