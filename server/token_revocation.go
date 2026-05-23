package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cnak-us/artifact-gateway/store"
	"github.com/google/uuid"
)

// TokenRevocationChecker resolves a customer_tokens row UUID to a
// (revoked|not-revoked|missing) verdict, used by BearerJWT to enforce that a
// rotated/revoked token immediately stops minting and serving OCI pulls.
//
// The check is on the hot path (every /v2/* request after the bearer is
// parsed) so we cache positive AND negative results with a short TTL. Rotate
// handlers call BumpEpoch after a successful rotate to invalidate every
// cached entry — the next request for any token re-reads from the DB.
//
// Cache TTL is set short (default 5s) so even without an explicit BumpEpoch
// (e.g. a manual admin DELETE on customer_tokens) revocations propagate
// quickly. The cache is bounded only by the number of distinct token row
// UUIDs that have made an OCI request in the last TTL window — for normal
// fleets this is a handful of entries.
type TokenRevocationChecker struct {
	store store.DataStore
	ttl   time.Duration

	mu      sync.RWMutex
	entries map[uuid.UUID]revokedEntry
	epoch   atomic.Uint64
}

type revokedEntry struct {
	revoked bool
	epoch   uint64
	expires time.Time
}

// NewTokenRevocationChecker returns a checker backed by st with the given
// cache TTL. A TTL of 0 disables caching (every request hits the DB).
func NewTokenRevocationChecker(st store.DataStore, ttl time.Duration) *TokenRevocationChecker {
	return &TokenRevocationChecker{
		store:   st,
		ttl:     ttl,
		entries: map[uuid.UUID]revokedEntry{},
	}
}

// BumpEpoch invalidates all cached entries. Call after any operation that
// changes a customer_tokens row's revoked_at (rotate, admin revoke). Safe to
// call concurrently.
func (c *TokenRevocationChecker) BumpEpoch() {
	c.epoch.Add(1)
}

// IsRevoked returns true when the row identified by tokenRowID has a non-NULL
// revoked_at, false when the row exists and is active. A missing row is
// treated as revoked (true, nil) — a JWT that names a row that no longer
// exists must not pass.
func (c *TokenRevocationChecker) IsRevoked(ctx context.Context, tokenRowID uuid.UUID) (bool, error) {
	if tokenRowID == uuid.Nil {
		return true, nil
	}
	curEpoch := c.epoch.Load()
	now := time.Now()
	if c.ttl > 0 {
		c.mu.RLock()
		e, ok := c.entries[tokenRowID]
		c.mu.RUnlock()
		if ok && e.epoch == curEpoch && now.Before(e.expires) {
			return e.revoked, nil
		}
	}

	tok, err := c.store.GetCustomerToken(ctx, tokenRowID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			c.cache(tokenRowID, true, curEpoch, now)
			return true, nil
		}
		return false, err
	}
	revoked := tok.RevokedAt != nil
	c.cache(tokenRowID, revoked, curEpoch, now)
	return revoked, nil
}

func (c *TokenRevocationChecker) cache(id uuid.UUID, revoked bool, epoch uint64, now time.Time) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	c.entries[id] = revokedEntry{revoked: revoked, epoch: epoch, expires: now.Add(c.ttl)}
	c.mu.Unlock()
}
