package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store Suite")
}

var _ = Describe("Postgres store", func() {
	var (
		ctx context.Context
		pg  *store.PG
	)

	BeforeEach(func() {
		dsn := os.Getenv("TEST_DATABASE_URL")
		if dsn == "" {
			Skip("TEST_DATABASE_URL not set; skipping integration test")
		}
		ctx = context.Background()

		var err error
		pg, err = store.New(ctx, dsn)
		Expect(err).NotTo(HaveOccurred())

		Expect(pg.EnsureSchema(ctx)).To(Succeed())
	})

	AfterEach(func() {
		if pg != nil {
			pg.Close()
		}
	})

	It("round-trips a package, license, and customer token", func() {
		// upstream credential first (FK target for package)
		uc := &store.UpstreamCredential{
			Name:           "test-ghcr-" + uuid.NewString(),
			Kind:           "ghcr",
			Username:       "tester",
			PATEnc:         []byte("ciphertext"),
			PATFingerprint: "deadbeef",
		}
		Expect(pg.InsertUpstreamCredential(ctx, uc)).To(Succeed())

		// package
		slug := "pkg-" + uuid.NewString()
		path := "path/" + uuid.NewString()
		pkg := &store.Package{
			Slug:                  slug,
			Path:                  path,
			UpstreamRepo:          "ghcr.io/cnak-us/cnak-core",
			UpstreamCredentialID:  uc.ID,
			Kind:                  "container",
			DisplayName:           "CNAK Core",
			Description:           "test pkg",
			ReleaseNotesURL:       "",
			InstallInstructionsMD: "# install",
		}
		Expect(pg.InsertPackage(ctx, pkg)).To(Succeed())

		got, err := pg.GetPackageBySlug(ctx, slug)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ID).To(Equal(pkg.ID))
		Expect(got.Path).To(Equal(path))
		Expect(got.UpstreamCredentialID).To(Equal(uc.ID))
		Expect(got.Kind).To(Equal("container"))

		// license
		exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
		lic := &store.License{
			LicenseID:             "lic-" + uuid.NewString(),
			Customer:              "Acme",
			Organization:          "Acme Org",
			Tier:                  "enterprise",
			ExpiresAt:             &exp,
			LicBlob:               "payload.signature",
			CustomerRotateEnabled: true,
		}
		Expect(pg.InsertLicense(ctx, lic)).To(Succeed())

		gotLic, err := pg.GetLicenseByLicenseID(ctx, lic.LicenseID)
		Expect(err).NotTo(HaveOccurred())
		Expect(gotLic.ID).To(Equal(lic.ID))
		Expect(gotLic.Tier).To(Equal("enterprise"))
		Expect(gotLic.ExpiresAt).NotTo(BeNil())
		Expect(gotLic.ExpiresAt.Equal(exp)).To(BeTrue())

		// customer token
		tok := &store.CustomerToken{
			TokenID:     "TKN" + uuid.NewString(),
			SecretHash:  "$2a$12$abcdefghijklmnopqrstuv",
			LicenseID:   lic.ID,
			Description: "ci token",
		}
		Expect(pg.InsertCustomerToken(ctx, tok)).To(Succeed())

		gotTok, err := pg.GetCustomerTokenByTokenID(ctx, tok.TokenID)
		Expect(err).NotTo(HaveOccurred())
		Expect(gotTok.ID).To(Equal(tok.ID))
		Expect(gotTok.LicenseID).To(Equal(lic.ID))

		// grants
		Expect(pg.ReplaceGrantsForLicense(ctx, lic.ID, []uuid.UUID{pkg.ID}, []string{"pull"})).To(Succeed())
		has, err := pg.HasGrant(ctx, lic.ID, pkg.ID, "pull")
		Expect(err).NotTo(HaveOccurred())
		Expect(has).To(BeTrue())

		granted, err := pg.GrantedPackagesForLicense(ctx, lic.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(granted).To(HaveLen(1))
		Expect(granted[0].ID).To(Equal(pkg.ID))

		// not-found mapping
		_, err = pg.GetPackage(ctx, uuid.New())
		Expect(err).To(MatchError(store.ErrNotFound))
	})
})
