// Package agentrelease answers which agent release `latest` is on this
// install, from the same sources GET /cdn/agent-versions serves.
//
// It exists so the fleet's "an agent update is available" answer and the
// version picker read one source. The alternative on offer was the browser's
// own fetch of /cdn/agent-versions, which every node row already does
// separately — fine for one sidebar, wrong for a fleet view that must give the
// same answer for 500 nodes at once.
package agentrelease

import (
	"context"
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/agentpkg"
)

// cacheTTL bounds how long an answer is reused. A release is a deliberate,
// infrequent act; an S3 ListObjectsV2 per page load per tenant is not worth
// the freshness.
const cacheTTL = 5 * time.Minute

// versionLister is the optional capability a storage backend has when it can
// enumerate the agent CDN. Named rather than asserted inline, per the ports
// convention.
type versionLister interface {
	ListAgentVersions(basePath string) ([]string, error)
}

// archVersionLister is the per-architecture listing, which both shipped
// backends have; with it `latest` is resolved for the fleet's architecture
// rather than from one marker for all of them.
type archVersionLister interface {
	ListAgentVersionsByArch(basePath string) (map[string][]string, error)
}

// Source serves the version `latest` resolves to.
//
// A nil *Source is a valid storage backend that cannot enumerate: Latest
// returns "" and no error, which tells the caller to skip the question rather
// than answer it.
type Source struct {
	store    storage.StorageClient
	lister   versionLister
	basePath string

	mu        sync.Mutex
	value     string
	fetchedAt time.Time
}

// NewSource returns a source over the storage client, or nil when that backend
// cannot list agent packages.
func NewSource(client storage.StorageClient, basePath string) *Source {
	lister, ok := client.(versionLister)
	if !ok {
		return nil
	}
	return &Source{store: client, lister: lister, basePath: basePath}
}

// Latest returns the concrete version `latest` installs on the fleet's
// architecture (agentpkg.DefaultArch), or "" when it cannot be determined.
//
// It is the version the release import recorded beside `latest`
// (agentpkg.LatestMarkerName) when there is one, because a one-click update
// dispatches `agent_version: latest`, and the newest LISTED version differs
// from it after an import that did not move `latest` — every node would then
// read as outdated while each update reinstalled the old package. Without a
// marker (an install that never imported) it is the newest listed version, as
// before. The "latest" alias itself is never returned: it is a file name, not
// a version, and comparing a node's version against it would call every node
// outdated forever.
func (s *Source) Latest(ctx context.Context) (string, error) {
	if s == nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value != "" && time.Since(s.fetchedAt) <= cacheTTL {
		return s.value, nil
	}
	var v string
	var err error
	if byArch, ok := s.lister.(archVersionLister); ok {
		var listed map[string][]string
		if listed, err = byArch.ListAgentVersionsByArch(s.basePath); err == nil {
			v = agentpkg.ResolveLatestForArch(ctx, s.store, s.basePath, agentpkg.DefaultArch, listed[agentpkg.DefaultArch])
		}
	} else {
		var versions []string
		if versions, err = s.lister.ListAgentVersions(s.basePath); err == nil {
			v = agentpkg.ResolveLatest(ctx, s.store, s.basePath, versions)
		}
	}
	if err != nil {
		// Keep serving a previous answer rather than telling the fleet its
		// agents are unknowable because one listing timed out.
		if s.value != "" {
			return s.value, nil
		}
		return "", err
	}
	if v != "" {
		s.value = v
		s.fetchedAt = time.Now()
	}
	return v, nil
}
