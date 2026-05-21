package auth_test

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/cnak-us/artifact-gateway/auth"
)

func TestAuth(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Auth Suite")
}

// randomHexKey returns a hex string of n random bytes — used to build a
// JWT signing key fixture.
func randomHexKey(n int) string {
	buf := make([]byte, n)
	_, err := rand.Read(buf)
	Expect(err).NotTo(HaveOccurred())
	return hex.EncodeToString(buf)
}

// randomBase64KEK returns a base64-encoded 32-byte KEK fixture.
func randomBase64KEK() string {
	buf := make([]byte, 32)
	_, err := rand.Read(buf)
	Expect(err).NotTo(HaveOccurred())
	return base64.StdEncoding.EncodeToString(buf)
}

var _ = Describe("Password", func() {
	It("hashes and verifies a password round-trip", func() {
		h, err := auth.HashPassword("correct horse battery staple")
		Expect(err).NotTo(HaveOccurred())
		Expect(h).NotTo(BeEmpty())
		Expect(auth.VerifyPassword(h, "correct horse battery staple")).To(Succeed())
	})

	It("rejects the wrong password with ErrInvalidPassword", func() {
		h, err := auth.HashPassword("hunter2")
		Expect(err).NotTo(HaveOccurred())
		err = auth.VerifyPassword(h, "hunter3")
		Expect(errors.Is(err, auth.ErrInvalidPassword)).To(BeTrue())
	})
})

var _ = Describe("Customer token", func() {
	// Charset of uppercase-no-padding base32 — used to assert the token_id
	// is shell/HTTP-Basic safe.
	const b32Charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

	It("generates a 20-char uppercase base32 token_id and decodable secret", func() {
		t, err := auth.GenerateCustomerToken()
		Expect(err).NotTo(HaveOccurred())

		Expect(t.TokenID).To(HaveLen(20))
		for _, r := range t.TokenID {
			Expect(strings.ContainsRune(b32Charset, r)).
				To(BeTrue(), "token_id char %q not in base32 charset", r)
		}

		Expect(t.Secret).NotTo(BeEmpty())
		_, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(t.Secret)
		Expect(err).NotTo(HaveOccurred())

		Expect(t.FullCredential).To(Equal(t.TokenID + ":" + t.Secret))
	})

	It("hashes and verifies the secret", func() {
		t, err := auth.GenerateCustomerToken()
		Expect(err).NotTo(HaveOccurred())

		h, err := auth.HashSecret(t.Secret)
		Expect(err).NotTo(HaveOccurred())
		Expect(auth.VerifySecret(h, t.Secret)).To(Succeed())

		err = auth.VerifySecret(h, "nope")
		Expect(errors.Is(err, auth.ErrInvalidSecret)).To(BeTrue())
	})

	It("produces a different token on every call", func() {
		a, err := auth.GenerateCustomerToken()
		Expect(err).NotTo(HaveOccurred())
		b, err := auth.GenerateCustomerToken()
		Expect(err).NotTo(HaveOccurred())
		Expect(a.TokenID).NotTo(Equal(b.TokenID))
		Expect(a.Secret).NotTo(Equal(b.Secret))
	})
})

var _ = Describe("ParseBasic", func() {
	encode := func(id, secret string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
	}

	It("parses a well-formed header", func() {
		id, secret, ok := auth.ParseBasic(encode("ABCD", "supersecret"))
		Expect(ok).To(BeTrue())
		Expect(id).To(Equal("ABCD"))
		Expect(secret).To(Equal("supersecret"))
	})

	It("accepts case-insensitive scheme name", func() {
		_, _, ok := auth.ParseBasic("basic " + base64.StdEncoding.EncodeToString([]byte("a:b")))
		Expect(ok).To(BeTrue())
	})

	It("rejects empty, prefix-only, non-Basic, malformed base64, and missing colon", func() {
		cases := []string{
			"",
			"Basic ",
			"Bearer abc",
			"Basic !!!notbase64!!!",
			"Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")),
			"Basic " + base64.StdEncoding.EncodeToString([]byte(":onlysecret")),
			"Basic " + base64.StdEncoding.EncodeToString([]byte("onlyid:")),
		}
		for _, h := range cases {
			_, _, ok := auth.ParseBasic(h)
			Expect(ok).To(BeFalse(), "expected reject for header %q", h)
		}
	})
})

var _ = Describe("JWTSigner", func() {
	const (
		issuer   = "artifact-gateway"
		audience = "artifacts.example.com"
	)
	access := []auth.Access{{Type: "repository", Name: "cnak-us/cnak-core", Actions: []string{"pull"}}}

	It("mints and verifies a JWT round-trip", func() {
		s, err := auth.NewJWTSigner(randomHexKey(32), issuer, audience, 5*time.Minute)
		Expect(err).NotTo(HaveOccurred())

		tok, expiresIn, iat, err := s.Mint("TOKID1234567890ABCDE", access)
		Expect(err).NotTo(HaveOccurred())
		Expect(tok).NotTo(BeEmpty())
		Expect(expiresIn).To(Equal(300))
		Expect(iat).To(BeTemporally("~", time.Now().UTC(), 2*time.Second))

		claims, err := s.Verify(tok)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims.Subject).To(Equal("TOKID1234567890ABCDE"))
		Expect(claims.Issuer).To(Equal(issuer))
		Expect(claims.Audience).To(ContainElement(audience))
		Expect(claims.ID).NotTo(BeEmpty())
		Expect(claims.Access).To(Equal(access))
	})

	It("rejects an expired token", func() {
		// Mint via the library directly so we can pre-date `exp`.
		secretHex := randomHexKey(32)
		s, err := auth.NewJWTSigner(secretHex, issuer, audience, time.Minute)
		Expect(err).NotTo(HaveOccurred())

		keyBytes, err := hex.DecodeString(secretHex)
		Expect(err).NotTo(HaveOccurred())

		past := time.Now().Add(-10 * time.Minute)
		claims := auth.OCIClaims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Subject:   "SUB",
				Audience:  jwt.ClaimStrings{audience},
				IssuedAt:  jwt.NewNumericDate(past),
				NotBefore: jwt.NewNumericDate(past),
				ExpiresAt: jwt.NewNumericDate(past.Add(time.Minute)),
				ID:        "jti-test",
			},
			Access: access,
		}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(keyBytes)
		Expect(err).NotTo(HaveOccurred())

		_, err = s.Verify(tok)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, jwt.ErrTokenExpired)).To(BeTrue())
	})

	It("rejects a token signed with the wrong key", func() {
		s1, err := auth.NewJWTSigner(randomHexKey(32), issuer, audience, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		s2, err := auth.NewJWTSigner(randomHexKey(32), issuer, audience, time.Minute)
		Expect(err).NotTo(HaveOccurred())

		tok, _, _, err := s1.Mint("SUB", access)
		Expect(err).NotTo(HaveOccurred())

		_, err = s2.Verify(tok)
		Expect(err).To(HaveOccurred())
	})

	It("rejects construction with an empty or bad key", func() {
		_, err := auth.NewJWTSigner("", issuer, audience, time.Minute)
		Expect(err).To(HaveOccurred())
		_, err = auth.NewJWTSigner("not-hex-zz", issuer, audience, time.Minute)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Crypto", func() {
	It("seals and opens a round-trip", func() {
		c, err := auth.NewCrypto(randomBase64KEK())
		Expect(err).NotTo(HaveOccurred())

		plaintext := []byte("ghp_thisIsAFakeGitHubPAT_0123456789")
		blob, err := c.Seal(plaintext)
		Expect(err).NotTo(HaveOccurred())
		Expect(blob).NotTo(Equal(plaintext))

		got, err := c.Open(blob)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(plaintext))
	})

	It("uses a fresh nonce per Seal so identical plaintexts produce distinct blobs", func() {
		c, err := auth.NewCrypto(randomBase64KEK())
		Expect(err).NotTo(HaveOccurred())

		a, err := c.Seal([]byte("same"))
		Expect(err).NotTo(HaveOccurred())
		b, err := c.Seal([]byte("same"))
		Expect(err).NotTo(HaveOccurred())
		Expect(a).NotTo(Equal(b))
	})

	It("rejects a tampered ciphertext", func() {
		c, err := auth.NewCrypto(randomBase64KEK())
		Expect(err).NotTo(HaveOccurred())

		blob, err := c.Seal([]byte("payload"))
		Expect(err).NotTo(HaveOccurred())
		// Flip a bit inside the ciphertext (past the 12-byte nonce).
		blob[len(blob)-1] ^= 0x01
		_, err = c.Open(blob)
		Expect(err).To(HaveOccurred())
	})

	It("rejects a too-short blob", func() {
		c, err := auth.NewCrypto(randomBase64KEK())
		Expect(err).NotTo(HaveOccurred())
		_, err = c.Open([]byte{0x00, 0x01})
		Expect(err).To(HaveOccurred())
	})

	It("rejects construction with a non-32-byte KEK", func() {
		short := base64.StdEncoding.EncodeToString(make([]byte, 16))
		_, err := auth.NewCrypto(short)
		Expect(err).To(HaveOccurred())

		_, err = auth.NewCrypto("not-base64!!!")
		Expect(err).To(HaveOccurred())

		_, err = auth.NewCrypto("")
		Expect(err).To(HaveOccurred())
	})

	It("Fingerprint returns 8 hex chars and is stable", func() {
		c, err := auth.NewCrypto(randomBase64KEK())
		Expect(err).NotTo(HaveOccurred())

		fp := c.Fingerprint([]byte("hello"))
		Expect(fp).To(HaveLen(8))
		Expect(fp).To(Equal(c.Fingerprint([]byte("hello"))))
		Expect(fp).NotTo(Equal(c.Fingerprint([]byte("HELLO"))))
	})
})
