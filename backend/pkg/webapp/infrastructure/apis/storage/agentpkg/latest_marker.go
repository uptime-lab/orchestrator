package agentpkg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
)

// LatestMarkerName is the LEGACY, architecture-agnostic marker: one object
// recording which concrete version `latest` IS, for every architecture at
// once. It was wrong for a release that carried one architecture only — the
// other architecture's `latest` packages did not move, yet the marker named
// the new version for both. The import no
// longer writes it unless the release covers every architecture the CDN
// serves; readers consult it only as a fallback, and only when the version
// it names is listed for the architecture asked about.
//
// Why a marker at all: `latest` is a literal file name, so an import that
// adds a newer version WITHOUT moving `latest` leaves the newest listed
// version and what a one-click update installs (`agent_version: latest`)
// different, and a fleet compared against the newest listed version then
// reads as outdated forever while every update reinstalls the old package.
const LatestMarkerName = "latest.version"

// DefaultArch is the architecture an answer without one describes: the
// fleet's (every supported board is arm64). The CDN listing's single
// `latest` field keeps meaning "what `latest` installs on arm64", which is
// what the deployed SPA has always compared a node against.
const DefaultArch = "arm64"

// LatestMarkerNameFor is the per-architecture marker,
// `latest.<arch>.version`, written by the release import (--set-latest) for
// exactly the architectures the release carries.
func LatestMarkerNameFor(arch string) string {
	return "latest." + arch + ".version"
}

// archRe is the shape an architecture segment may take in a marker name.
var archRe = regexp.MustCompile(`^[a-z0-9]+$`)

// maxLatestMarkerBytes bounds the read; a version string is a few bytes.
const maxLatestMarkerBytes = 256

// markerVersionRe is the shape a marker may carry: the CDN's version shape
// without the `v`.
var markerVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

// ValidMarkerVersion reports whether v may be written into, or read from, the
// marker.
func ValidMarkerVersion(v string) bool {
	return markerVersionRe.MatchString(v)
}

// ReadLatestMarker returns the version the LEGACY marker records, or "" when
// there is none. A marker that exists but is unreadable or malformed is an
// error, so a caller can fall back knowingly rather than silently.
func ReadLatestMarker(ctx context.Context, store storage.StorageClient, basePath string) (string, error) {
	return readMarker(ctx, store, ObjectKey(basePath, LatestMarkerName))
}

// ReadLatestMarkerFor returns the version arch's marker records, or "" when
// there is none.
func ReadLatestMarkerFor(ctx context.Context, store storage.StorageClient, basePath, arch string) (string, error) {
	if !archRe.MatchString(arch) {
		return "", errors.New("not an architecture name")
	}
	return readMarker(ctx, store, ObjectKey(basePath, LatestMarkerNameFor(arch)))
}

func readMarker(ctx context.Context, store storage.StorageClient, key string) (string, error) {
	info, err := store.GetFileInfo(ctx, key)
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", nil
	}
	r, err := store.Retrieve(ctx, key)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, maxLatestMarkerBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxLatestMarkerBytes {
		return "", errors.New("latest marker is larger than a version string")
	}
	v := strings.TrimSpace(buf.String())
	if !ValidMarkerVersion(v) {
		return "", errors.New("latest marker does not hold a version")
	}
	return v, nil
}

// ResolveLatest answers which concrete version `latest` is from the legacy
// marker and an architecture-agnostic listing: the marker when present and
// readable, else the newest concrete version listed. "" when neither
// answers. Kept for a storage backend that cannot list by architecture;
// prefer ResolveLatestForArch.
func ResolveLatest(ctx context.Context, store storage.StorageClient, basePath string, versions []string) string {
	if store != nil {
		if v, err := ReadLatestMarker(ctx, store, basePath); err == nil && v != "" {
			return v
		}
	}
	return newestConcrete(versions)
}

// ResolveLatestForArch answers which concrete version `latest` installs on
// arch, given that arch's listed versions (VersionsByArch): arch's own marker
// when present and readable; else the legacy marker, but only when the
// version it names is listed for arch (a partial-arch import wrote it for an
// architecture whose `latest` it did not move); else the newest version
// listed for arch. "" when nothing answers.
func ResolveLatestForArch(ctx context.Context, store storage.StorageClient, basePath, arch string, archVersions []string) string {
	if store != nil {
		if v, err := ReadLatestMarkerFor(ctx, store, basePath, arch); err == nil && v != "" {
			return v
		}
		if v, err := ReadLatestMarker(ctx, store, basePath); err == nil && v != "" {
			for _, listed := range archVersions {
				if listed == v {
					return v
				}
			}
		}
	}
	return newestConcrete(archVersions)
}

// ResolveLatestByArch resolves every architecture of a by-arch listing.
func ResolveLatestByArch(ctx context.Context, store storage.StorageClient, basePath string, byArch map[string][]string) map[string]string {
	out := make(map[string]string, len(byArch))
	for arch, versions := range byArch {
		if v := ResolveLatestForArch(ctx, store, basePath, arch, versions); v != "" {
			out[arch] = v
		}
	}
	return out
}

func newestConcrete(versions []string) string {
	for _, v := range versions {
		if v != LatestVersion {
			return v
		}
	}
	return ""
}
