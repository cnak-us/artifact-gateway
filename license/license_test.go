package license_test

import (
	"errors"
	"testing"
	"time"

	"github.com/cnak-us/artifact-gateway/license"
	pkglicense "github.com/cnak-us/artifact-gateway/internal/pkglicense"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLicense(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "License Suite")
}

func mkLic(id, expiresRFC3339 string) *pkglicense.License {
	return &pkglicense.License{
		ID:        id,
		Customer:  "Acme",
		Tier:      pkglicense.TierEnterprise,
		MaxTracks: 100,
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: expiresRFC3339,
	}
}

var _ = Describe("CheckActive", func() {
	It("returns nil for a perpetual license with no revocation", func() {
		l := mkLic("lic-1", "")
		Expect(license.CheckActive(l, nil, "lic-1")).To(Succeed())
	})

	It("returns nil when expectedID is empty (skip mismatch check)", func() {
		l := mkLic("lic-1", "")
		Expect(license.CheckActive(l, nil, "")).To(Succeed())
	})

	It("returns ErrParse when license is nil", func() {
		err := license.CheckActive(nil, nil, "lic-1")
		Expect(errors.Is(err, license.ErrParse)).To(BeTrue())
	})

	It("returns ErrRevoked when revokedAt is non-nil", func() {
		l := mkLic("lic-1", "")
		now := time.Now()
		err := license.CheckActive(l, &now, "lic-1")
		Expect(errors.Is(err, license.ErrRevoked)).To(BeTrue())
	})

	It("returns ErrExpired when ExpiresAt is in the past", func() {
		past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		l := mkLic("lic-1", past)
		err := license.CheckActive(l, nil, "lic-1")
		Expect(errors.Is(err, license.ErrExpired)).To(BeTrue())
	})

	It("accepts a license that expires in the future", func() {
		future := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
		l := mkLic("lic-1", future)
		Expect(license.CheckActive(l, nil, "lic-1")).To(Succeed())
	})

	It("returns ErrMismatch when expectedID does not match parsed ID", func() {
		l := mkLic("lic-1", "")
		err := license.CheckActive(l, nil, "lic-2")
		Expect(errors.Is(err, license.ErrMismatch)).To(BeTrue())
	})
})

// fakeVerifier lets us exercise VerifyAndCheck without a real signed blob.
type fakeVerifier struct {
	out *license.License
	err error
}

func (f fakeVerifier) VerifyLicenseBlob(_ string) (*license.License, error) {
	return f.out, f.err
}

var _ = Describe("VerifyAndCheck", func() {
	It("returns parse error untouched when verification fails", func() {
		v := fakeVerifier{err: license.ErrInvalidSignature}
		_, err := license.VerifyAndCheck(v, "irrelevant", nil, "lic-1")
		Expect(errors.Is(err, license.ErrInvalidSignature)).To(BeTrue())
	})

	It("returns the parsed license + nil on success", func() {
		l := mkLic("lic-7", "")
		v := fakeVerifier{out: l}
		got, err := license.VerifyAndCheck(v, "blob", nil, "lic-7")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(l))
	})

	It("returns the parsed license + ErrRevoked when store says revoked", func() {
		l := mkLic("lic-7", "")
		v := fakeVerifier{out: l}
		now := time.Now()
		got, err := license.VerifyAndCheck(v, "blob", &now, "lic-7")
		Expect(errors.Is(err, license.ErrRevoked)).To(BeTrue())
		Expect(got).To(Equal(l)) // still returned for logging
	})
})

var _ = Describe("Cache", func() {
	It("returns false on miss", func() {
		c := license.NewCache(nil, nil)
		defer c.Close()
		_, ok := c.Get("nope")
		Expect(ok).To(BeFalse())
	})

	It("returns the stored license after Put", func() {
		c := license.NewCache(nil, nil)
		defer c.Close()
		l := mkLic("lic-99", "")
		c.Put("lic-99", l)

		got, ok := c.Get("lic-99")
		Expect(ok).To(BeTrue())
		Expect(got).To(Equal(l))
	})

	It("returns false after Invalidate", func() {
		c := license.NewCache(nil, nil)
		defer c.Close()
		l := mkLic("lic-99", "")
		c.Put("lic-99", l)
		c.Invalidate("lic-99")
		_, ok := c.Get("lic-99")
		Expect(ok).To(BeFalse())
	})

	It("ignores empty IDs / nil licenses on Put", func() {
		c := license.NewCache(nil, nil)
		defer c.Close()
		c.Put("", mkLic("x", ""))
		c.Put("k", nil)
		_, ok := c.Get("")
		Expect(ok).To(BeFalse())
		_, ok = c.Get("k")
		Expect(ok).To(BeFalse())
	})
})
