package license

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// LicenseUpdatedSubject is the NATS subject the cache subscribes to.
// Publishers (the admin write path, NATS-backed JetStream consumers, etc.)
// should publish the license ID as the message body.
const LicenseUpdatedSubject = "cnak.internal.license.updated"

// CacheTTL is the positive-cache lifetime. NATS invalidations are best-effort,
// so we never trust an entry older than this. Kept short on purpose: token
// mints check the cache, and we want revocations to take effect promptly.
const CacheTTL = 30 * time.Second

type cacheEntry struct {
	lic       *License
	expiresAt time.Time
}

// Cache is a tiny in-process map of parsed licenses keyed by license ID. It
// avoids re-parsing the .lic blob (ed25519 verify) on every token mint.
// Safe for concurrent use.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	logger  *slog.Logger
	nc      *nats.Conn
	sub     *nats.Subscription
}

// NewCache constructs a cache. If nc is non-nil, it subscribes to
// LicenseUpdatedSubject and invalidates entries whose ID matches the message
// body. nc may be nil (e.g. in tests) — the TTL still bounds staleness.
func NewCache(nc *nats.Conn, logger *slog.Logger) *Cache {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Cache{
		entries: make(map[string]cacheEntry),
		logger:  logger,
		nc:      nc,
	}
	if nc != nil {
		sub, err := nc.Subscribe(LicenseUpdatedSubject, func(m *nats.Msg) {
			id := strings.TrimSpace(string(m.Data))
			if id == "" {
				return
			}
			c.Invalidate(id)
			c.logger.Debug("license cache invalidated via NATS", "license_id", id)
		})
		if err != nil {
			c.logger.Warn("license cache: NATS subscribe failed; relying on TTL only",
				"subject", LicenseUpdatedSubject, "error", err)
		} else {
			c.sub = sub
		}
	}
	return c
}

// Get returns the cached license for licenseID. The second return is true only
// when the entry exists AND has not exceeded CacheTTL.
func (c *Cache) Get(licenseID string) (*License, bool) {
	c.mu.RLock()
	e, ok := c.entries[licenseID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiresAt) {
		// Lazily purge expired entries to keep the map small.
		c.mu.Lock()
		if cur, still := c.entries[licenseID]; still && !time.Now().Before(cur.expiresAt) {
			delete(c.entries, licenseID)
		}
		c.mu.Unlock()
		return nil, false
	}
	return e.lic, true
}

// Put stores l under licenseID with a fresh TTL.
func (c *Cache) Put(licenseID string, l *License) {
	if licenseID == "" || l == nil {
		return
	}
	c.mu.Lock()
	c.entries[licenseID] = cacheEntry{
		lic:       l,
		expiresAt: time.Now().Add(CacheTTL),
	}
	c.mu.Unlock()
}

// Invalidate removes the entry for licenseID, if any.
func (c *Cache) Invalidate(licenseID string) {
	if licenseID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, licenseID)
	c.mu.Unlock()
}

// Close unsubscribes from NATS (if subscribed) and clears the cache.
func (c *Cache) Close() {
	c.mu.Lock()
	c.entries = make(map[string]cacheEntry)
	sub := c.sub
	c.sub = nil
	c.mu.Unlock()
	if sub != nil {
		_ = sub.Unsubscribe()
	}
}
