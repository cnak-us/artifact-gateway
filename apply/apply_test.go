package apply_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/cnak-us/artifact-gateway/apply"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/license"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeVerifier echoes the blob's first segment as the parsed license ID so
// tests can construct license payloads without going through the real
// cnaklic signer.
type fakeVerifier struct {
	rejectIDs map[string]bool
}

func (f *fakeVerifier) VerifyLicenseBlob(raw string) (*license.License, error) {
	if f.rejectIDs[raw] {
		return nil, errors.New("signature invalid")
	}
	parts := strings.SplitN(raw, "|", 4)
	if len(parts) < 2 {
		return nil, errors.New("malformed test license")
	}
	return &license.License{
		ID:       parts[0],
		Customer: parts[1],
		Tier:     "enterprise",
	}, nil
}

// freshKEK returns a base64-encoded 32-byte key (deterministic for tests so
// we can decrypt-and-verify if a future test needs to).
func freshKEK() string {
	var k [32]byte
	for i := range k {
		k[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(k[:])
}

func newCrypto() *auth.Crypto {
	c, err := auth.NewCrypto(freshKEK())
	Expect(err).ToNot(HaveOccurred())
	return c
}

var _ = Describe("Parse", func() {
	It("accepts a minimal YAML manifest", func() {
		raw := `
apiVersion: artifact-gateway.cnak.us/v1
kind: ArtifactGatewayConfig
metadata:
  name: default
spec: {}
`
		mf, err := apply.Parse([]byte(raw))
		Expect(err).ToNot(HaveOccurred())
		Expect(mf.APIVersion).To(Equal(apply.APIVersion))
		Expect(mf.Kind).To(Equal(apply.Kind))
	})

	It("accepts JSON", func() {
		raw := `{"apiVersion":"artifact-gateway.cnak.us/v1","kind":"ArtifactGatewayConfig","spec":{}}`
		mf, err := apply.Parse([]byte(raw))
		Expect(err).ToNot(HaveOccurred())
		Expect(mf.Kind).To(Equal(apply.Kind))
	})

	It("rejects an unknown apiVersion", func() {
		raw := `apiVersion: other/v2
kind: ArtifactGatewayConfig
spec: {}`
		_, err := apply.Parse([]byte(raw))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("apiVersion"))
	})

	It("rejects an empty body", func() {
		_, err := apply.Parse([]byte("   \n  "))
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Resolve", func() {
	It("drains *FromEnv references into plaintext fields", func() {
		GinkgoT().Setenv("TEST_PW", "supersecret")
		GinkgoT().Setenv("TEST_CLIENT_SECRET", "client-secret-value")
		GinkgoT().Setenv("TEST_PAT", "ghp_abc")
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				StaticAdmins: []apply.StaticAdminSpec{
					{Email: "ops@x", PasswordFromEnv: "TEST_PW"},
				},
				OIDCProviders: []apply.OIDCProviderSpec{
					{Name: "dex", ClientSecretFromEnv: "TEST_CLIENT_SECRET"},
				},
				UpstreamCredentials: []apply.UpstreamCredentialSpec{
					{Name: "ghcr", PATFromEnv: "TEST_PAT"},
				},
			},
		}
		Expect(apply.Resolve(mf)).To(Succeed())
		Expect(mf.Spec.StaticAdmins[0].Password).To(Equal("supersecret"))
		Expect(mf.Spec.OIDCProviders[0].ClientSecret).To(Equal("client-secret-value"))
		Expect(mf.Spec.UpstreamCredentials[0].PAT).To(Equal("ghp_abc"))
	})

	It("aggregates every missing env reference into one error", func() {
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				StaticAdmins: []apply.StaticAdminSpec{
					{Email: "a@x", PasswordFromEnv: "MISSING_A"},
					{Email: "b@x", PasswordFromEnv: "MISSING_B"},
				},
			},
		}
		err := apply.Resolve(mf)
		Expect(err).To(HaveOccurred())
		Expect(apply.IsMissingEnv(err)).To(BeTrue())
		var miss *apply.MissingEnvError
		Expect(errors.As(err, &miss)).To(BeTrue())
		Expect(miss.Refs).To(HaveLen(2))
		Expect(miss.Refs[0]).To(ContainSubstring("MISSING_A"))
		Expect(miss.Refs[1]).To(ContainSubstring("MISSING_B"))
	})
})

var _ = Describe("Reconcile", func() {
	var (
		ctx    context.Context
		st     *fakeStore
		crypto *auth.Crypto
		verif  *fakeVerifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		st = newFakeStore()
		crypto = newCrypto()
		verif = &fakeVerifier{}
	})

	It("creates upstream credentials, packages, licenses, grants, and providers from scratch", func() {
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				UpstreamCredentials: []apply.UpstreamCredentialSpec{
					{Name: "ghcr", Kind: "ghcr", Username: "bot", PAT: "tok"},
				},
				Packages: []apply.PackageSpec{
					{
						Slug: "core", Source: "oci",
						Path: "ns/core", UpstreamRepo: "ns/core",
						UpstreamCredential: "ghcr", Kind: "container",
						DisplayName: "Core",
					},
				},
				Licenses: []apply.LicenseSpec{
					{LicBlob: "lic_1|Acme|enterprise|2030-01-01"},
				},
				Grants: []apply.GrantSpec{
					{License: "lic_1", Packages: []string{"core"}},
				},
				OIDCProviders: []apply.OIDCProviderSpec{
					{
						Name: "dex", IssuerURL: "https://dex.example.com",
						ClientID: "ag", ClientSecret: "shh",
						Scopes: []string{"openid", "email"}, Enabled: true,
					},
				},
				StaticAdmins: []apply.StaticAdminSpec{
					{Email: "ops@example.com", Password: "p"},
				},
			},
		}
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.Errors).To(BeEmpty())

		actions := actionsByKey(rep)
		Expect(actions["upstream-credential/ghcr"]).To(Equal("create"))
		Expect(actions["package/core"]).To(Equal("create"))
		Expect(actions["license/lic_1"]).To(Equal("create"))
		Expect(actions["grant/lic_1"]).To(Equal("create"))
		Expect(actions["oidc-provider/dex"]).To(Equal("create"))
		Expect(actions["static-admin/ops@example.com"]).To(Equal("create"))
	})

	It("is idempotent: a second apply is all-noop", func() {
		mf := minimalManifest()
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		rep2, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep2.Errors).To(BeEmpty())
		for _, it := range rep2.Items {
			Expect(it.Action).To(Equal("noop"),
				"item %s/%s expected noop, got %s", it.Kind, it.Name, it.Action)
		}
	})

	It("reports diffs without writing under DryRun=true", func() {
		mf := minimalManifest()
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		mf.Spec.Packages[0].DisplayName = "Brand New"
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{DryRun: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.DryRun).To(BeTrue())

		var pkgItem *apply.ApplyItem
		for i := range rep.Items {
			if rep.Items[i].Kind == apply.KindPackage && rep.Items[i].Name == "core" {
				pkgItem = &rep.Items[i]
			}
		}
		Expect(pkgItem).ToNot(BeNil())
		Expect(pkgItem.Action).To(Equal("update"))
		Expect(pkgItem.Diff).To(ContainElement("display_name"))

		// And the DB still shows the old value — dry-run wrote nothing.
		pkgs, _ := st.ListPackages(ctx)
		Expect(pkgs[0].DisplayName).To(Equal("Core"))
	})

	It("prunes manifest-managed static admins when Prune=true", func() {
		// Seed two admins.
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				StaticAdmins: []apply.StaticAdminSpec{
					{Email: "keep@x", Password: "k"},
					{Email: "drop@x", Password: "d"},
				},
			},
		}
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		// Drop one from the manifest; re-apply with Prune.
		mf.Spec.StaticAdmins = mf.Spec.StaticAdmins[:1]
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{Prune: true})
		Expect(err).ToNot(HaveOccurred())

		actions := actionsByKey(rep)
		Expect(actions["static-admin/drop@x"]).To(Equal("delete"))

		got, _ := st.ListStaticAdmins(ctx)
		Expect(got).To(HaveLen(1))
		Expect(strings.ToLower(got[0].Email)).To(Equal("keep@x"))
	})

	It("rejects a package whose upstreamCredential is unknown", func() {
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				Packages: []apply.PackageSpec{
					{
						Slug: "core", Source: "oci", Path: "ns/core",
						UpstreamRepo: "ns/core", UpstreamCredential: "nope",
						Kind: "container",
					},
				},
			},
		}
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.Errors).To(HaveLen(1))
		Expect(rep.Errors[0].Kind).To(Equal(apply.KindPackage))
		Expect(rep.Errors[0].Message).To(ContainSubstring(`"nope"`))
	})

	It("rejects a grant referencing a license that doesn't exist", func() {
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				Grants: []apply.GrantSpec{
					{License: "lic_ghost", Packages: []string{"core"}},
				},
			},
		}
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.Errors).To(HaveLen(1))
		Expect(rep.Errors[0].Kind).To(Equal(apply.KindGrant))
		Expect(rep.Errors[0].Message).To(ContainSubstring("lic_ghost"))
	})

	It("records a per-license error when the .lic blob is invalid", func() {
		verif.rejectIDs = map[string]bool{"bad-blob": true}
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				Licenses: []apply.LicenseSpec{{LicBlob: "bad-blob"}},
			},
		}
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.Errors).To(HaveLen(1))
		Expect(rep.Errors[0].Kind).To(Equal(apply.KindLicense))
		Expect(rep.Errors[0].Message).To(ContainSubstring("invalid"))
	})

	It("updates an oidc provider's client_secret when changed", func() {
		mf := minimalManifest()
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		mf.Spec.OIDCProviders[0].ClientSecret = "rotated"
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		actions := actionsByKey(rep)
		Expect(actions["oidc-provider/dex"]).To(Equal("update"))
		var item *apply.ApplyItem
		for i := range rep.Items {
			if rep.Items[i].Kind == apply.KindOIDCProvider {
				item = &rep.Items[i]
			}
		}
		Expect(item).ToNot(BeNil())
		Expect(item.Diff).To(ContainElement("client_secret"))
	})
})

func minimalManifest() *apply.Manifest {
	return &apply.Manifest{
		APIVersion: apply.APIVersion, Kind: apply.Kind,
		Spec: apply.ManifestSpec{
			UpstreamCredentials: []apply.UpstreamCredentialSpec{
				{Name: "ghcr", Kind: "ghcr", Username: "bot", PAT: "tok"},
			},
			Packages: []apply.PackageSpec{
				{
					Slug: "core", Source: "oci",
					Path: "ns/core", UpstreamRepo: "ns/core",
					UpstreamCredential: "ghcr", Kind: "container",
					DisplayName: "Core",
				},
			},
			OIDCProviders: []apply.OIDCProviderSpec{
				{
					Name: "dex", IssuerURL: "https://dex.example.com",
					ClientID: "ag", ClientSecret: "shh",
					Scopes: []string{"openid"}, Enabled: true,
				},
			},
		},
	}
}

func actionsByKey(rep *apply.ApplyReport) map[string]string {
	out := make(map[string]string, len(rep.Items))
	for _, it := range rep.Items {
		out[fmt.Sprintf("%s/%s", it.Kind, it.Name)] = it.Action
	}
	return out
}
