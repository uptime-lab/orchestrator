package eepromsigner

import (
	"context"
	"sync"
	"time"
)

// BaseCacheTTL bounds how long the signer's /base reply is reused.
const BaseCacheTTL = 5 * time.Minute

// BaseCache serves the signer's firmware base from a short process-wide cache.
//
// It is one shared instance on purpose. Two readers now ask this question —
// the /eeprom/base-firmware route the node sidebar polls, and the fleet
// attention check, which asks it once for the whole account — and a second
// cache with its own TTL would let the same install answer "up to date" in one
// view and "update available" in the other for minutes at a time.
//
// A nil *BaseCache is a valid no-signer install: Versions returns nothing and
// no error, which is the caller's cue to skip the bootloader question rather
// than answer it.
type BaseCache struct {
	client *Client

	mu        sync.Mutex
	value     map[string]BaseFirmware
	fetchedAt time.Time
}

// NewBaseCache returns a cache over the signer, or nil when there is none.
func NewBaseCache(client *Client) *BaseCache {
	if client == nil {
		return nil
	}
	return &BaseCache{client: client}
}

// Versions returns the firmware base per SoC, and whether the answer is a
// stale cache served because the signer could not be reached.
//
// On a signer failure with nothing cached it returns the error: the caller
// decides whether that is a 502 or a skipped check. With something cached it
// serves the stale copy rather than flapping — the firmware base changes on a
// release cadence, so minutes-old is right and "unknown" is not.
func (c *BaseCache) Versions(ctx context.Context) (fw map[string]BaseFirmware, stale bool, err error) {
	if c == nil {
		return nil, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != nil && time.Since(c.fetchedAt) <= BaseCacheTTL {
		return c.value, false, nil
	}
	fresh, err := c.client.BaseFirmwareVersions(ctx)
	if err != nil {
		if c.value == nil {
			return nil, false, err
		}
		return c.value, true, nil
	}
	c.value = fresh
	c.fetchedAt = time.Now()
	return c.value, false, nil
}

// BaseVersions is Versions reduced to the version hash per SoC, the only part
// a freshness comparison needs. An empty map means "no signer, or it could not
// be read" — the caller must skip the bootloader question, not answer it.
func (c *BaseCache) BaseVersions(ctx context.Context) (map[string]string, error) {
	fw, _, err := c.Versions(ctx)
	if err != nil {
		return nil, err
	}
	if len(fw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(fw))
	for soc, f := range fw {
		if f.Version != "" {
			out[soc] = f.Version
		}
	}
	return out, nil
}
