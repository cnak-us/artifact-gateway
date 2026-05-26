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
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"

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
		GinkgoT().Setenv("TEST_PAT", "ghp_abc")
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				UpstreamCredentials: []apply.UpstreamCredentialSpec{
					{Name: "ghcr", PATFromEnv: "TEST_PAT"},
				},
			},
		}
		Expect(apply.Resolve(mf)).To(Succeed())
		Expect(mf.Spec.UpstreamCredentials[0].PAT).To(Equal("ghp_abc"))
	})

	It("aggregates every missing env reference into one error", func() {
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				UpstreamCredentials: []apply.UpstreamCredentialSpec{
					{Name: "a", PATFromEnv: "MISSING_A"},
					{Name: "b", PATFromEnv: "MISSING_B"},
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

	It("creates upstream credentials, packages, licenses, and grants from scratch", func() {
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

	// --- prune correctness ---------------------------------------------------

	Describe("Prune", func() {
		It("deletes upstream credentials tagged source='manifest' that vanish from the manifest", func() {
			mf := &apply.Manifest{
				APIVersion: apply.APIVersion, Kind: apply.Kind,
				Spec: apply.ManifestSpec{
					UpstreamCredentials: []apply.UpstreamCredentialSpec{
						{Name: "keep", Kind: "ghcr", Username: "bot", PAT: "k"},
						{Name: "drop", Kind: "ghcr", Username: "bot", PAT: "d"},
					},
				},
			}
			_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
			Expect(err).ToNot(HaveOccurred())

			// Drop one cred, re-apply with Prune.
			mf.Spec.UpstreamCredentials = mf.Spec.UpstreamCredentials[:1]
			rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{Prune: true})
			Expect(err).ToNot(HaveOccurred())
			Expect(actionsByKey(rep)["upstream-credential/drop"]).To(Equal("delete"))

			got, _ := st.ListUpstreamCredentials(ctx)
			Expect(got).To(HaveLen(1))
			Expect(got[0].Name).To(Equal("keep"))
		})

		It("LEAVES admin-UI-created credentials (source='') alone when pruning", func() {
			// Seed an admin-UI-created row directly into the store.
			Expect(st.InsertUpstreamCredential(ctx, &store.UpstreamCredential{
				ID: uuid.New(), Name: "admin-cred", Kind: "ghcr",
				Username: "user", PATFingerprint: "fp", Source: "", // legacy / admin-UI tag
			})).To(Succeed())

			// Apply a manifest that doesn't mention "admin-cred", with prune on.
			mf := &apply.Manifest{
				APIVersion: apply.APIVersion, Kind: apply.Kind,
				Spec: apply.ManifestSpec{
					UpstreamCredentials: []apply.UpstreamCredentialSpec{
						{Name: "mani", Kind: "ghcr", Username: "bot", PAT: "tok"},
					},
				},
			}
			_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{Prune: true})
			Expect(err).ToNot(HaveOccurred())

			got, _ := st.ListUpstreamCredentials(ctx)
			names := make([]string, 0, len(got))
			for _, c := range got {
				names = append(names, c.Name)
			}
			Expect(names).To(ConsistOf("admin-cred", "mani"))
		})

		It("LEAVES admin-UI-created packages (managed_by='') alone when pruning", func() {
			// Seed a cred so the admin package can FK to something.
			credID := uuid.New()
			Expect(st.InsertUpstreamCredential(ctx, &store.UpstreamCredential{
				ID: credID, Name: "shared-cred", Kind: "ghcr", Username: "u", Source: "",
			})).To(Succeed())
			Expect(st.InsertPackage(ctx, &store.Package{
				ID: uuid.New(), Slug: "admin-pkg", Path: "p/admin", UpstreamRepo: "p/admin",
				UpstreamCredentialID: credID, Kind: "container", Source: "oci",
				ManagedBy: "", // admin-UI
			})).To(Succeed())

			mf := &apply.Manifest{
				APIVersion: apply.APIVersion, Kind: apply.Kind,
				Spec: apply.ManifestSpec{
					UpstreamCredentials: []apply.UpstreamCredentialSpec{
						{Name: "shared-cred", Kind: "ghcr", Username: "u", PAT: "t"},
					},
				},
			}
			_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{Prune: true})
			Expect(err).ToNot(HaveOccurred())

			got, _ := st.ListPackages(ctx)
			Expect(got).To(HaveLen(1))
			Expect(got[0].Slug).To(Equal("admin-pkg"))
		})

		It("LEAVES admin-UI-created licenses (source='') alone when pruning", func() {
			Expect(st.InsertLicense(ctx, &store.License{
				ID: uuid.New(), LicenseID: "lic_admin", Customer: "Admin", LicBlob: "blob", Source: "", CustomerRotateEnabled: true,
			})).To(Succeed())

			mf := &apply.Manifest{
				APIVersion: apply.APIVersion, Kind: apply.Kind,
				Spec: apply.ManifestSpec{
					Licenses: []apply.LicenseSpec{
						{LicBlob: "lic_manifest|Mani"},
					},
				},
			}
			_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{Prune: true})
			Expect(err).ToNot(HaveOccurred())

			got, _ := st.ListLicenses(ctx)
			ids := make([]string, 0, len(got))
			for _, l := range got {
				ids = append(ids, l.LicenseID)
			}
			Expect(ids).To(ConsistOf("lic_admin", "lic_manifest"))
		})

		It("removes both a credential and its packages from the manifest with Prune=true (FK order)", func() {
			// Seed via the reconciler so both rows are tagged manifest-managed.
			mf := &apply.Manifest{
				APIVersion: apply.APIVersion, Kind: apply.Kind,
				Spec: apply.ManifestSpec{
					UpstreamCredentials: []apply.UpstreamCredentialSpec{
						{Name: "uc1", Kind: "ghcr", Username: "u", PAT: "t"},
					},
					Packages: []apply.PackageSpec{
						{
							Slug: "pkg1", Source: "oci",
							Path: "ns/p1", UpstreamRepo: "ns/p1",
							UpstreamCredential: "uc1", Kind: "container",
						},
					},
				},
			}
			_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
			Expect(err).ToNot(HaveOccurred())

			// Drop both. fakeStore enforces the FK so the prune ordering must
			// delete the package before the credential.
			st.enforceFK = true
			empty := &apply.Manifest{APIVersion: apply.APIVersion, Kind: apply.Kind}
			rep, err := apply.Reconcile(ctx, st, crypto, verif, empty, apply.Options{Prune: true})
			Expect(err).ToNot(HaveOccurred())
			Expect(rep.Errors).To(BeEmpty(), "FK ordering should not produce errors; got %+v", rep.Errors)

			gotPkgs, _ := st.ListPackages(ctx)
			gotCreds, _ := st.ListUpstreamCredentials(ctx)
			Expect(gotPkgs).To(BeEmpty())
			Expect(gotCreds).To(BeEmpty())
		})

		It("clears existing grants when a license is kept but its grants[] is emptied", func() {
			mf := &apply.Manifest{
				APIVersion: apply.APIVersion, Kind: apply.Kind,
				Spec: apply.ManifestSpec{
					UpstreamCredentials: []apply.UpstreamCredentialSpec{
						{Name: "ghcr", Kind: "ghcr", Username: "u", PAT: "t"},
					},
					Packages: []apply.PackageSpec{
						{Slug: "core", Source: "oci", Path: "n/c", UpstreamRepo: "n/c", UpstreamCredential: "ghcr", Kind: "container"},
					},
					Licenses: []apply.LicenseSpec{{LicBlob: "lic_1|Acme|enterprise|2030"}},
					Grants:   []apply.GrantSpec{{License: "lic_1", Packages: []string{"core"}}},
				},
			}
			_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
			Expect(err).ToNot(HaveOccurred())

			// Drop the grant entry but keep the license.
			mf.Spec.Grants = nil
			rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{Prune: true})
			Expect(err).ToNot(HaveOccurred())
			Expect(actionsByKey(rep)["grant/lic_1"]).To(Equal("delete"))

			lic := findLicense(ctx, st, "lic_1")
			Expect(lic).ToNot(BeNil())
			grants, _ := st.ListGrantsForLicense(ctx, lic.ID)
			Expect(grants).To(BeEmpty())
		})
	})
})

var _ = Describe("Reconcile containers", func() {
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

	multiContainerManifest := func(containers ...apply.ContainerSpec) *apply.Manifest {
		return &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				UpstreamCredentials: []apply.UpstreamCredentialSpec{
					{Name: "ghcr", Kind: "ghcr", Username: "bot", PAT: "tok"},
				},
				Packages: []apply.PackageSpec{
					{
						Slug: "cnak-platform", Source: "oci",
						Path:               "cnak-platform",
						UpstreamCredential: "ghcr", Kind: "container",
						DisplayName: "CNAK Platform",
						Containers:  containers,
					},
				},
			},
		}
	}

	// Pull the row UUID for cnak-platform out of the fake store, so we can
	// directly seed UI containers under the same package.
	pkgRowID := func() uuid.UUID {
		pkgs, _ := st.ListPackages(ctx)
		for _, p := range pkgs {
			if p.Slug == "cnak-platform" {
				return p.ID
			}
		}
		return uuid.Nil
	}

	It("inserts each declared container with source='manifest'", func() {
		mf := multiContainerManifest(
			apply.ContainerSpec{Alias: "backend", UpstreamRepo: "ns/backend"},
			apply.ContainerSpec{Alias: "worker", UpstreamRepo: "ns/worker", DisplayName: "Worker"},
		)
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.Errors).To(BeEmpty())

		actions := actionsByKey(rep)
		Expect(actions["container/cnak-platform/backend"]).To(Equal("create"))
		Expect(actions["container/cnak-platform/worker"]).To(Equal("create"))

		got, err := st.ListContainersForPackage(ctx, pkgRowID())
		Expect(err).ToNot(HaveOccurred())
		Expect(got).To(HaveLen(2))
		for _, c := range got {
			Expect(c.Source).To(Equal("manifest"))
		}
	})

	It("is a noop on a second apply with the same containers", func() {
		mf := multiContainerManifest(
			apply.ContainerSpec{Alias: "backend", UpstreamRepo: "ns/backend"},
			apply.ContainerSpec{Alias: "worker", UpstreamRepo: "ns/worker"},
		)
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.Errors).To(BeEmpty())
		actions := actionsByKey(rep)
		Expect(actions["container/cnak-platform/backend"]).To(Equal("noop"))
		Expect(actions["container/cnak-platform/worker"]).To(Equal("noop"))
	})

	It("deletes a container removed from the manifest, leaving the other survivor", func() {
		mf := multiContainerManifest(
			apply.ContainerSpec{Alias: "backend", UpstreamRepo: "ns/backend"},
			apply.ContainerSpec{Alias: "worker", UpstreamRepo: "ns/worker"},
		)
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		mf.Spec.Packages[0].Containers = mf.Spec.Packages[0].Containers[:1]
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(actionsByKey(rep)["container/cnak-platform/worker"]).To(Equal("delete"))

		got, _ := st.ListContainersForPackage(ctx, pkgRowID())
		aliases := []string{}
		for _, c := range got {
			aliases = append(aliases, c.Alias)
		}
		Expect(aliases).To(ConsistOf("backend"))
	})

	It("preserves a UI-created container (source='') across manifest reapply", func() {
		// Seed the package row first by running an apply that creates it.
		mf := multiContainerManifest(
			apply.ContainerSpec{Alias: "backend", UpstreamRepo: "ns/backend"},
		)
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		// Seed a UI container on the same package, directly.
		Expect(st.UpsertContainer(ctx, &store.PackageContainer{
			PackageID:    pkgRowID(),
			Alias:        "ui-only",
			UpstreamRepo: "ns/ui-only",
			Source:       "",
		})).To(Succeed())

		// Reapply with one manifest container — UI row should survive.
		_, err = apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		got, _ := st.ListContainersForPackage(ctx, pkgRowID())
		aliases := []string{}
		for _, c := range got {
			aliases = append(aliases, c.Alias)
		}
		Expect(aliases).To(ConsistOf("backend", "ui-only"))
	})

	It("records ApplyError for invalid aliases but does not abort siblings", func() {
		mf := multiContainerManifest(
			apply.ContainerSpec{Alias: "", UpstreamRepo: "ns/empty"},
			apply.ContainerSpec{Alias: "has/slash", UpstreamRepo: "ns/slash"},
			apply.ContainerSpec{Alias: "has space", UpstreamRepo: "ns/space"},
			apply.ContainerSpec{Alias: "good", UpstreamRepo: "ns/good"},
		)
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(len(rep.Errors)).To(BeNumerically(">=", 3))
		Expect(actionsByKey(rep)["container/cnak-platform/good"]).To(Equal("create"))

		got, _ := st.ListContainersForPackage(ctx, pkgRowID())
		Expect(got).To(HaveLen(1))
		Expect(got[0].Alias).To(Equal("good"))
	})

	It("reports an error for a duplicate alias within one package spec; first wins", func() {
		mf := multiContainerManifest(
			apply.ContainerSpec{Alias: "backend", UpstreamRepo: "ns/first"},
			apply.ContainerSpec{Alias: "backend", UpstreamRepo: "ns/second"},
		)
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		var dupErr bool
		for _, e := range rep.Errors {
			if e.Kind == apply.KindContainer && strings.Contains(e.Message, "duplicate") {
				dupErr = true
			}
		}
		Expect(dupErr).To(BeTrue())

		got, _ := st.ListContainersForPackage(ctx, pkgRowID())
		Expect(got).To(HaveLen(1))
		Expect(got[0].UpstreamRepo).To(Equal("ns/first"))
	})

	It("writes empty upstream_repo on the package row when containers are present", func() {
		mf := multiContainerManifest(
			apply.ContainerSpec{Alias: "backend", UpstreamRepo: "ns/backend"},
		)
		// Also set UpstreamRepo on the package spec; it should be ignored.
		mf.Spec.Packages[0].UpstreamRepo = "ns/should-be-ignored"
		_, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())

		pkgs, _ := st.ListPackages(ctx)
		Expect(pkgs).To(HaveLen(1))
		Expect(pkgs[0].UpstreamRepo).To(Equal(""))
	})

	It("still accepts legacy single-container packages (no containers, with upstreamRepo)", func() {
		mf := &apply.Manifest{
			APIVersion: apply.APIVersion, Kind: apply.Kind,
			Spec: apply.ManifestSpec{
				UpstreamCredentials: []apply.UpstreamCredentialSpec{
					{Name: "ghcr", Kind: "ghcr", Username: "bot", PAT: "tok"},
				},
				Packages: []apply.PackageSpec{
					{
						Slug: "legacy", Source: "oci",
						Path:               "legacy",
						UpstreamRepo:       "ns/legacy",
						UpstreamCredential: "ghcr", Kind: "container",
					},
				},
			},
		}
		rep, err := apply.Reconcile(ctx, st, crypto, verif, mf, apply.Options{})
		Expect(err).ToNot(HaveOccurred())
		Expect(rep.Errors).To(BeEmpty())

		pkgs, _ := st.ListPackages(ctx)
		Expect(pkgs[0].UpstreamRepo).To(Equal("ns/legacy"))
	})
})

func findLicense(ctx context.Context, st *fakeStore, licID string) *store.License {
	all, _ := st.ListLicenses(ctx)
	for i := range all {
		if all[i].LicenseID == licID {
			return &all[i]
		}
	}
	return nil
}

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
