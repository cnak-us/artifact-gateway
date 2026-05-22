// artifact-gateway — a thin OCI auth gateway that proxies ghcr.io for
// license-gated customer access. See README.md and the plan at
// /Users/wcrum/.claude/plans/linked-exploring-sutton.md for the overall design.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/cnak-us/artifact-gateway/apply"
	"github.com/cnak-us/artifact-gateway/audit"
	"github.com/cnak-us/artifact-gateway/auth"
	"github.com/cnak-us/artifact-gateway/config"
	"github.com/cnak-us/artifact-gateway/license"
	"github.com/cnak-us/artifact-gateway/metrics"
	agoidc "github.com/cnak-us/artifact-gateway/oidc"
	"github.com/cnak-us/artifact-gateway/server"
	"github.com/cnak-us/artifact-gateway/store"
	"github.com/cnak-us/artifact-gateway/ui"
)

var version = "dev"

func main() {
	cfg := config.LoadFromEnv()

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	logger.Info("artifact-gateway starting",
		"version", version,
		"public_port", cfg.PublicPort,
		"mgmt_port", cfg.ManagementPort,
		"external_hostname", cfg.ExternalHostname,
	)

	if cfg.DatabaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ---- store ----
	st, err := store.New(rootCtx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect postgres", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.EnsureSchema(rootCtx); err != nil {
		logger.Error("ensure schema", "err", err)
		os.Exit(1)
	}

	// ---- secrets ----
	crypto, err := auth.NewCrypto(cfg.KEKBase64)
	if err != nil {
		logger.Error("KEK_BASE64 invalid (need 32 base64 bytes)", "err", err)
		os.Exit(1)
	}

	signer, err := auth.NewJWTSigner(
		cfg.JWTSigningKey,
		"artifact-gateway",
		cfg.ExternalHostname,
		time.Duration(cfg.TokenTTLSeconds)*time.Second,
	)
	if err != nil {
		logger.Error("JWT_SIGNING_KEY invalid (need hex bytes)", "err", err)
		os.Exit(1)
	}

	sessions, err := agoidc.NewManager(cfg.SessionSigningKey, "ag_admin_session", cfg.CookieSecure)
	if err != nil {
		logger.Error("SESSION_SIGNING_KEY invalid (need hex bytes)", "err", err)
		os.Exit(1)
	}
	catalogSessions, err := agoidc.NewManager(cfg.SessionSigningKey, "ag_customer_session", cfg.CookieSecure)
	if err != nil {
		logger.Error("init catalog session manager", "err", err)
		os.Exit(1)
	}

	// ---- bootstrap admin ----
	if err := bootstrapAdmin(rootCtx, cfg, st, logger); err != nil {
		logger.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}

	// ---- NATS (optional) ----
	var nc *nats.Conn
	if cfg.NATSURL != "" {
		opts := []nats.Option{
			nats.Name("artifact-gateway"),
			nats.MaxReconnects(-1),
			nats.ReconnectWait(2 * time.Second),
		}
		if cfg.NATSCredsFile != "" {
			opts = append(opts, nats.UserCredentials(cfg.NATSCredsFile))
		} else if cfg.NATSAuthToken != "" {
			opts = append(opts, nats.Token(cfg.NATSAuthToken))
		}
		nc, err = nats.Connect(cfg.NATSURL, opts...)
		if err != nil {
			logger.Warn("NATS connect failed — continuing without audit fanout / license cache invalidation", "err", err)
		} else {
			defer nc.Close()
			logger.Info("NATS connected", "url", cfg.NATSURL)
		}
	}

	// ---- auditor + license cache + verifier ----
	auditor := audit.NewAuditor(nc, st, logger)
	verifier := license.NewStoreVerifier(func() []ed25519.PublicKey {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rows, err := st.ListRootKeys(ctx)
		if err != nil {
			logger.Warn("list root keys for verifier failed", "err", err)
			return nil
		}
		out := make([]ed25519.PublicKey, 0, len(rows))
		for i := range rows {
			if len(rows[i].PublicKey) == ed25519.PublicKeySize {
				out = append(out, ed25519.PublicKey(rows[i].PublicKey))
			}
		}
		return out
	})
	licCache := license.NewCache(nc, logger)

	// ---- OIDC ----
	publicURL := externalPublicURL(cfg)
	// Discovery URL overrides — used when the in-network DNS name the gateway
	// uses to reach Dex differs from the browser-visible issuer URL (compose
	// dev). DEX_DISCOVERY_URL=http://dex:5556 + DEX_ISSUER_URL=http://localhost:5556
	// is the canonical setup; if the env var isn't set the registry uses the
	// row's issuer URL for both browser and server-side, which is right for
	// `make dev` (gateway on host) and production (single hostname).
	discoveryOverrides := map[string]string{}
	if disc := strings.TrimSpace(os.Getenv("DEX_DISCOVERY_URL")); disc != "" {
		discoveryOverrides["dex"] = disc
	}
	oidcRegistry := agoidc.NewRegistry(st, crypto, publicURL, logger, discoveryOverrides)
	oidcHandlers, err := agoidc.NewHandlerDeps(
		oidcRegistry, sessions, st, auditor,
		cfg.SessionSigningKey, cfg.OIDCAutoprovision, cfg.CookieSecure, logger,
	)
	if err != nil {
		logger.Error("init oidc handlers", "err", err)
		os.Exit(1)
	}

	// ---- bootstrap from CONFIG_FILE (declarative manifest) ----
	// Runs AFTER the crypto/verifier/oidcRegistry are wired but BEFORE the
	// OIDC reload so any providers the manifest creates are picked up by the
	// reload below. Hard-exits on error: a deployment shipping with a bad
	// config should fail loudly, not silently start with stale state.
	if path := os.Getenv("CONFIG_FILE"); path != "" {
		if err := bootstrapFromFile(rootCtx, path, st, crypto, verifier, logger); err != nil {
			logger.Error("CONFIG_FILE apply failed — startup aborted", "path", path, "err", err)
			os.Exit(1)
		}
	}

	// ---- bootstrap Dex provider (idempotent) ----
	// Inserts a "dex" OIDC provider row when DEX_ISSUER_URL, DEX_CLIENT_ID, and
	// DEX_CLIENT_SECRET are all set AND no "dex" provider row exists yet.
	bootstrapDexProvider(rootCtx, cfg, st, crypto, logger)

	// Load OIDC providers from the DB. Without this, the registry is empty
	// at startup and every /api/v1/auth/oidc/:name/start returns "unknown
	// provider" until the process restarts.
	if err := oidcRegistry.Reload(rootCtx); err != nil {
		logger.Warn("oidc reload at startup failed — login-with-OIDC disabled until next reload", "err", err)
	} else {
		logger.Info("oidc providers loaded", "count", len(oidcRegistry.Names()))
	}

	// ---- metrics collector ----
	// Pre-touch every known label set so counters/histograms show up at value 0
	// from the start; otherwise the UI catalog and /metrics output would be
	// empty until first traffic. Then start an in-process ring buffer of
	// Prometheus snapshots that backs the admin UI charts (external scraping
	// via /metrics on the management port still works).
	metrics.Init()
	metricsCollector := metrics.NewCollector(nil, 5*time.Second, 720)
	go metricsCollector.Run(rootCtx)

	// ---- upstream proxy ----
	upstream := server.NewUpstream(crypto, st, auditor, logger)

	// ---- public router ----
	publicRouter := chi.NewRouter()
	publicRouter.Use(middleware.RealIP)
	publicRouter.Use(server.RequestID)
	publicRouter.Use(server.Logger(logger))
	publicRouter.Use(middleware.Recoverer)

	server.MountOCI(publicRouter, server.Deps{
		Store:    st,
		Signer:   signer,
		Crypto:   crypto,
		Cache:    licCache,
		Verifier: verifier,
		Auditor:  auditor,
		Cfg:      cfg,
		Upstream: upstream,
		Logger:   logger,
	})

	// Wire the PostAuthCallback for the auto-login flow (Dex-first).
	oidcHandlers.PostAuthCallback = buildPostAuthCallback(rootCtx, cfg, st, sessions, catalogSessions, auditor, logger)

	server.MountAdmin(publicRouter, server.AdminDeps{
		Store:           st,
		Crypto:          crypto,
		Signer:          signer,
		Verifier:        verifier,
		Auditor:         auditor,
		Sessions:        sessions,
		OIDC:            oidcHandlers,
		OIDCRegistry:    oidcRegistry,
		Metrics:         metricsCollector,
		Cfg:             cfg,
		Logger:          logger,
		Upstream:        upstream,
		CatalogSessions: catalogSessions,
	})

	upstream.StartIssuerRefresh(rootCtx)

	catalogDeps := server.CatalogDeps{
		Store:               st,
		Crypto:              crypto,
		Cache:               licCache,
		Verifier:            verifier,
		Auditor:             auditor,
		Sessions:            catalogSessions,
		Upstream:            upstream,
		Cfg:                 cfg,
		Logger:              logger,
		OIDCDefaultProvider: cfg.OIDCDefaultProvider,
	}
	server.MountCatalog(publicRouter, catalogDeps)
	server.MountCatalogOIDC(publicRouter, catalogDeps, oidcHandlers, oidcRegistry)

	gh := &server.GitHubReleasesClient{Client: &http.Client{Timeout: 15 * time.Second}}
	gl := &server.GitLabReleasesClient{Client: &http.Client{Timeout: 15 * time.Second}}
	server.MountDownloads(publicRouter, server.DownloadsDeps{
		Store:    st,
		Crypto:   crypto,
		Signer:   signer,
		GH:       gh,
		GL:       gl,
		Cfg:      cfg,
		Auditor:  auditor,
		Sessions: catalogSessions,
		Verifier: verifier,
		Logger:   logger,
	})

	mountUI(publicRouter, logger)

	// ---- management router ----
	mgmtRouter := chi.NewRouter()
	mgmtRouter.Use(middleware.Recoverer)
	mgmtRouter.Get("/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mgmtRouter.Get("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := st.Pool().Ping(ctx); err != nil {
			http.Error(w, "postgres unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mgmtRouter.Handle("/metrics", metrics.Handler())

	// ---- run + signal handling ----
	srv := server.New(cfg, publicRouter, mgmtRouter, logger)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutdown signal received")
		cancel()
	}()

	if err := srv.ListenAndServe(rootCtx); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}

// newLogger builds a slog handler honoring LOG_LEVEL + LOG_FORMAT.
func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.ToLower(cfg.LogFormat) == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	l := slog.New(h)
	if cfg.PodName != "" {
		l = l.With("pod", cfg.PodName)
	}
	return l
}

// bootstrapAdmin creates the configured admin user if no users exist yet.
// Idempotent on startup — does nothing once any user is present.
func bootstrapAdmin(ctx context.Context, cfg *config.Config, st store.DataStore, logger *slog.Logger) error {
	if cfg.AdminBootstrapEmail == "" || cfg.AdminBootstrapPassword == "" {
		return nil
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if n > 0 {
		return nil
	}
	hash, err := auth.HashPassword(cfg.AdminBootstrapPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	now := time.Now().UTC()
	u := &store.User{
		ID:           uuid.New(),
		Email:        strings.ToLower(cfg.AdminBootstrapEmail),
		PasswordHash: hash,
		Role:         "admin",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := st.InsertUser(ctx, u); err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	logger.Warn("bootstrap admin created — CHANGE THE PASSWORD AFTER FIRST LOGIN", "email", u.Email)
	return nil
}

// externalPublicURL derives a base URL for OIDC callbacks. Best-effort; admins
// can override by setting EXTERNAL_HOSTNAME to a fully-qualified URL.
func externalPublicURL(cfg *config.Config) string {
	host := cfg.ExternalHostname
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}
	scheme := "https"
	if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.") {
		scheme = "http"
	}
	return scheme + "://" + host
}

// bootstrapFromFile reads a declarative ArtifactGatewayConfig manifest from
// disk and applies it via the apply package. Honors the CONFIG_FILE_PRUNE
// env var (default false) — set to "true"/"1" so manifest-managed rows are
// the ONLY rows in the DB after apply (matches GitOps reconcile semantics).
//
// Errors are returned as-is; the caller hard-exits the process so air-gapped
// /GitOps shops fail loudly when bad config ships rather than starting with
// the previous (now-stale) DB state.
func bootstrapFromFile(
	ctx context.Context,
	path string,
	st store.DataStore,
	crypto *auth.Crypto,
	verifier license.Verifier,
	logger *slog.Logger,
) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	mf, err := apply.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if err := apply.Resolve(mf); err != nil {
		return fmt.Errorf("resolve env refs in %s: %w", path, err)
	}
	opts := apply.Options{Prune: getEnvBoolMain("CONFIG_FILE_PRUNE", false)}
	report, err := apply.Reconcile(ctx, st, crypto, verifier, mf, opts)
	if err != nil {
		return fmt.Errorf("reconcile %s: %w", path, err)
	}
	// Per-item logging so operators see exactly what changed at boot.
	for _, it := range report.Items {
		logger.Info("config-file apply",
			"kind", it.Kind, "name", it.Name, "action", it.Action, "diff", it.Diff)
	}
	if len(report.Errors) > 0 {
		for _, e := range report.Errors {
			logger.Error("config-file apply error",
				"kind", e.Kind, "name", e.Name, "msg", e.Message)
		}
		return fmt.Errorf("%d items failed to apply", len(report.Errors))
	}
	logger.Info("config-file apply complete",
		"path", path, "items", len(report.Items), "prune", opts.Prune)
	return nil
}

// bootstrapDexProvider inserts a "dex" OIDC provider row when DEX_ISSUER_URL,
// DEX_CLIENT_ID, and DEX_CLIENT_SECRET are all present AND no "dex" provider
// row already exists. Idempotent. Errors are logged as warnings and do not
// fail startup.
func bootstrapDexProvider(ctx context.Context, cfg *config.Config, st store.DataStore, crypto *auth.Crypto, logger *slog.Logger) {
	issuerURL := os.Getenv("DEX_ISSUER_URL")
	clientID := os.Getenv("DEX_CLIENT_ID")
	clientSecret := os.Getenv("DEX_CLIENT_SECRET")
	if issuerURL == "" || clientID == "" || clientSecret == "" {
		return
	}
	// Check if a "dex" provider already exists.
	existing, err := st.GetOIDCProviderByName(ctx, "dex")
	if err == nil && existing != nil {
		logger.Info("dex provider already registered — skipping bootstrap")
		return
	}
	sealed, err := crypto.Seal([]byte(clientSecret))
	if err != nil {
		logger.Warn("dex bootstrap: seal client secret failed", "err", err)
		return
	}
	row := &store.OIDCProvider{
		ID:              uuid.New(),
		Name:            "dex",
		IssuerURL:       issuerURL,
		ClientID:        clientID,
		ClientSecretEnc: sealed,
		Scopes:          []string{"openid", "profile", "email"},
		Enabled:         true,
	}
	if err := st.InsertOIDCProvider(ctx, row); err != nil {
		logger.Warn("dex bootstrap: insert provider failed", "err", err)
		return
	}
	logger.Info("dex provider bootstrapped", "issuer_url", issuerURL)
}

// buildPostAuthCallback returns the PostAuthCallback hook for the auto-login
// flow. It is invoked after a successful Dex code exchange (flow=auto) and
// issues a real session immediately. There is no /choose chooser, no pending
// cookie, and no license-entitlement gate at session-issue time:
//
//   - Every Dex-authenticated user always receives an ag_customer_session
//     (Role=customer, UserID=uuid.Nil). Users without a license still get a
//     working catalog session; they just see an empty catalog. License gating
//     happens at package list/pull time, not here.
//   - Static admins (env STATIC_ADMINS or DB static_admins) and DB users whose
//     row has Role=="admin" additionally get an ag_admin_session and land on
//     /admin. Everyone else lands on /catalog.
func buildPostAuthCallback(
	_ context.Context,
	cfg *config.Config,
	st store.DataStore,
	sessions *agoidc.Manager,
	catalogSessions *agoidc.Manager,
	auditor *audit.Auditor,
	logger *slog.Logger,
) func(http.ResponseWriter, *http.Request, string, string, string, string) {
	_ = logger
	return func(w http.ResponseWriter, r *http.Request, providerName, email, _subject, returnTo string) {
		ctx := r.Context()
		ip := clientIPFromRequest(r)

		// --- compute isAdmin only ---
		// Order matters: env-configured static admins and DB-configured
		// static admins (both bypass the users table) come first so an
		// operator-bootstrap email like admin@cnak.us is granted admin
		// even if no users row was created by bootstrapAdmin yet.
		// Autoprovision is NOT an admin signal — newly-created users start
		// as plain customers via the always-issued customer session below.
		isAdmin := false
		isStaticAdmin := false
		var adminUserID uuid.UUID
		var adminRole string

		if _, ok := cfg.StaticAdmins[email]; ok {
			isAdmin = true
			isStaticAdmin = true
		}
		if !isAdmin {
			if _, err := st.GetStaticAdminByEmail(ctx, email); err == nil {
				isAdmin = true
				isStaticAdmin = true
			}
		}
		if !isAdmin {
			if u, err := st.GetUserByEmail(ctx, email); err == nil && u.DisabledAt == nil && u.Role == "admin" {
				isAdmin = true
				adminUserID = u.ID
				adminRole = u.Role
			}
		}
		if isStaticAdmin {
			adminUserID = uuid.Nil
			adminRole = "admin"
		}

		// --- always issue a customer session ---
		// Dex auth alone is enough to log in. Users without a license still
		// get a working catalog session; resolveLicenseForSession returns an
		// empty list, so the catalog just renders empty. License gating lives
		// at package list/pull time, not here.
		if err := catalogSessions.Issue(w, agoidc.Session{
			UserID:   uuid.Nil,
			Email:    email,
			Role:     "customer",
			CanAdmin: isAdmin,
		}); err != nil {
			http.Error(w, "session issue failed", http.StatusInternalServerError)
			return
		}

		// --- mint admin session if applicable, redirect to /admin ---
		// CanCustomer is set to true because we always mint a customer session
		// alongside the admin session in this branch — the admin UI uses this
		// flag to render the "Catalog" shortcut in the top bar.
		if isAdmin {
			if err := sessions.Issue(w, agoidc.Session{
				UserID:      adminUserID,
				Email:       email,
				Role:        adminRole,
				CanCustomer: true,
			}); err != nil {
				http.Error(w, "session issue failed", http.StatusInternalServerError)
				return
			}
			if auditor != nil {
				auditor.LogAdminLogin(email, "oidc-auto:"+providerName, ip, "success")
			}
			http.Redirect(w, r, autoDest(returnTo, "/admin"), http.StatusFound)
			return
		}

		if auditor != nil {
			auditor.LogAdminLogin(email, "oidc-auto:"+providerName, ip, "success:catalog")
		}
		http.Redirect(w, r, autoDest(returnTo, "/catalog"), http.StatusFound)
	}
}

// autoDest picks the post-auth redirect destination. /choose was a legacy
// role-chooser page that has been removed; any inbound returnTo beginning with
// "/choose" is rewritten to the role default in case an older bookmark or
// cached redirect still references it. Empty or non-relative returnTo also
// defaults. sanitizeReturnTo upstream is the primary guard; this is defense
// in depth.
func autoDest(returnTo, defaultPath string) string {
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		return defaultPath
	}
	if strings.HasPrefix(returnTo, "/choose") {
		return defaultPath
	}
	return returnTo
}

// clientIPFromRequest extracts the best-effort client IP from a request,
// preferring X-Forwarded-For when present. Mirrors the oidc package's remoteIP.
func clientIPFromRequest(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

// getEnvBoolMain mirrors config.getEnvBool — kept local to avoid widening
// the config package's API surface for one boot-time flag.
func getEnvBoolMain(key string, defaultValue bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return defaultValue
	}
	return v == "true" || v == "1" || v == "yes"
}

// mountUI serves the embedded React build at /admin/* and /catalog/* with
// SPA index.html fallback. Static asset paths (/assets/*) pass through to the
// embedded filesystem directly.
func mountUI(r chi.Router, logger *slog.Logger) {
	uifs, err := ui.FS()
	if err != nil {
		logger.Warn("UI filesystem unavailable — admin/catalog routes will 404", "err", err)
		return
	}
	fileServer := http.FileServer(http.FS(uifs))

	// Serve everything else as the SPA: try the file, fall back to index.html.
	spa := func(w http.ResponseWriter, req *http.Request) {
		clean := strings.TrimPrefix(req.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		if _, err := fs.Stat(uifs, clean); errors.Is(err, fs.ErrNotExist) {
			// SPA route — serve index.html so the client-side router handles it.
			req2 := req.Clone(req.Context())
			req2.URL.Path = "/"
			fileServer.ServeHTTP(w, req2)
			return
		}
		fileServer.ServeHTTP(w, req)
	}

	r.Get("/", spa)
	r.Get("/admin", spa)
	r.Get("/admin/*", spa)
	r.Get("/catalog", spa)
	r.Get("/catalog/*", spa)
	r.Get("/assets/*", fileServer.ServeHTTP)
	// Anything else not matched by the API/OCI routes (favicon, robots.txt,
	// brand assets dropped into ui/src/public/, etc.) falls through here.
	// spa() serves the file if it exists in the embed and otherwise returns
	// the SPA index for client-side routing.
	r.NotFound(spa)
}
