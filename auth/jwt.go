package auth

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Access is a single repository:actions grant in the OCI bearer JWT, per the
// Docker registry token JWT spec.
type Access struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// ociClaimsVersion is the current OCIClaims schema version. Verify rejects
// tokens whose `v` claim is not equal to this; bump when the claim shape
// changes in a way that requires existing tokens to be rotated out.
const ociClaimsVersion = 2

// OCIClaims is the full claim set of a minted bearer JWT.
//
// TokenRowID is the customer_tokens.id (UUID, not the public token_id) and
// lets the BearerJWT middleware enforce row-level revocation on every OCI
// request. A rotated/revoked row immediately invalidates the JWT regardless
// of its remaining TTL — required so rotation actually rotates.
//
// Ver pins the claim schema so a future incompatible change can reject older
// tokens cleanly.
type OCIClaims struct {
	jwt.RegisteredClaims
	Access     []Access  `json:"access"`
	Ver        int       `json:"v"`
	TokenRowID uuid.UUID `json:"trid,omitempty"`
}

// JWTSigner mints and verifies OCI bearer JWTs (HMAC-SHA256).
type JWTSigner struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
}

// NewJWTSigner constructs a signer. secretHex is hex-decoded to raw HMAC key
// bytes; non-hex or empty input is rejected. ttl is the token lifetime (and
// the `expires_in` returned by Mint).
func NewJWTSigner(secretHex string, issuer, audience string, ttl time.Duration) (*JWTSigner, error) {
	if secretHex == "" {
		return nil, errors.New("auth: jwt signing key is empty")
	}
	key, err := hex.DecodeString(secretHex)
	if err != nil {
		return nil, fmt.Errorf("auth: jwt signing key is not valid hex: %w", err)
	}
	if len(key) == 0 {
		return nil, errors.New("auth: jwt signing key decoded to zero bytes")
	}
	if issuer == "" {
		return nil, errors.New("auth: jwt issuer is empty")
	}
	if audience == "" {
		return nil, errors.New("auth: jwt audience is empty")
	}
	if ttl <= 0 {
		return nil, errors.New("auth: jwt ttl must be positive")
	}
	return &JWTSigner{
		secret:   key,
		issuer:   issuer,
		audience: audience,
		ttl:      ttl,
	}, nil
}

// Mint produces a signed JWT for subject (the customer token_id) bound to the
// customer_tokens row identified by tokenRowID with the given access grants.
// Returns the encoded token, its lifetime in seconds (for the OCI
// token-response `expires_in`), and the issuedAt timestamp.
//
// tokenRowID must be non-nil; an all-zero UUID is rejected so the BearerJWT
// middleware can rely on it for revocation lookups.
func (s *JWTSigner) Mint(subject string, tokenRowID uuid.UUID, access []Access) (string, int, time.Time, error) {
	if tokenRowID == uuid.Nil {
		return "", 0, time.Time{}, errors.New("auth: jwt mint requires non-nil token row id")
	}
	now := time.Now().UTC()
	exp := now.Add(s.ttl)
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", 0, time.Time{}, err
	}
	claims := OCIClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{s.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        jti.String(),
		},
		Access:     access,
		Ver:        ociClaimsVersion,
		TokenRowID: tokenRowID,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	return signed, int(s.ttl.Seconds()), now, nil
}

// DownloadURLClaims is the claim set used for short-lived signed download
// URLs (the browser flow for non-OCI artifact downloads). Path binds the
// token to a single download URL so a leaked token can't be retargeted.
type DownloadURLClaims struct {
	jwt.RegisteredClaims
	Path string `json:"path"`
}

const downloadURLAudience = "artifact-gateway/download"

// SignDownloadURL mints a short-lived JWT bound to subject and a specific
// download path. The token's audience is "artifact-gateway/download" — the
// signing key is shared with the OCI signer, but the audience separation
// makes the two token kinds non-interchangeable.
//
// Caller chooses ttl: 60-120s is the design recommendation.
func (s *JWTSigner) SignDownloadURL(subject, path string, ttl time.Duration) (string, time.Time, error) {
	if subject == "" {
		return "", time.Time{}, errors.New("auth: download url subject is empty")
	}
	if path == "" {
		return "", time.Time{}, errors.New("auth: download url path is empty")
	}
	if ttl <= 0 {
		return "", time.Time{}, errors.New("auth: download url ttl must be positive")
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", time.Time{}, err
	}
	claims := DownloadURLClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{downloadURLAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        jti.String(),
		},
		Path: path,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(s.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// VerifyDownloadURL parses + validates a download-URL JWT. Audience and
// issuer are checked; the caller MUST additionally enforce that the
// returned Path matches the URL the client is hitting.
func (s *JWTSigner) VerifyDownloadURL(token string) (*DownloadURLClaims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(downloadURLAudience),
		jwt.WithExpirationRequired(),
	)
	claims := &DownloadURLClaims{}
	_, err := parser.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// Verify parses and validates a token against the signer's key, issuer, and
// audience. It returns the populated claim set on success. Tokens whose `v`
// claim is missing or does not equal ociClaimsVersion are rejected — this
// forces in-flight pre-upgrade tokens to be re-minted.
func (s *JWTSigner) Verify(token string) (*OCIClaims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.audience),
		jwt.WithExpirationRequired(),
	)
	claims := &OCIClaims{}
	_, err := parser.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if claims.Ver != ociClaimsVersion {
		return nil, fmt.Errorf("auth: jwt schema version %d not accepted (want %d)", claims.Ver, ociClaimsVersion)
	}
	if claims.TokenRowID == uuid.Nil {
		return nil, errors.New("auth: jwt missing token row id")
	}
	return claims, nil
}
