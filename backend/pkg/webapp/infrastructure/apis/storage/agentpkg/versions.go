// Package agentpkg parses agent package filenames into the version list the
// agent CDN advertises.
//
// It exists so that the local-filesystem and S3 storage backends answer
// `GET /cdn/agent-versions` identically. The logic used to live inside the
// local client, where a second backend could only have copied it — and a copy
// of a version sort is exactly the kind of thing that drifts silently and is
// noticed as "the picker lists 0.4.9 above 0.4.10" long after the fact.
package agentpkg

import (
	"regexp"
	"sort"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// pkgRe matches orchestrator-agent_<version>_linux_<arch>.<ext> filenames.
var pkgRe = regexp.MustCompile(`^orchestrator-agent_(.+)_linux_([a-z0-9]+)\.(deb|rpm|tar\.gz)$`)

// Versions extracts the distinct agent versions from a list of file names
// (base names, not paths). "latest" is placed first if present, the rest sorted
// descending so the newest release is near the top.
func Versions(names []string) []string {
	var versions []string
	for _, name := range names {
		if m := pkgRe.FindStringSubmatch(name); m != nil {
			versions = append(versions, m[1])
		}
	}
	return ordered(versions)
}

// VersionsByArch is Versions per architecture: which versions of the agent
// each architecture can actually install. A release imported for one
// architecture only lists under that architecture alone, which is what lets
// `latest` be resolved per architecture (ResolveLatestForArch).
func VersionsByArch(names []string) map[string][]string {
	byArch := map[string][]string{}
	for _, name := range names {
		if m := pkgRe.FindStringSubmatch(name); m != nil {
			byArch[m[2]] = append(byArch[m[2]], m[1])
		}
	}
	for arch, versions := range byArch {
		byArch[arch] = ordered(versions)
	}
	return byArch
}

// ordered deduplicates and sorts as Versions documents.
func ordered(in []string) []string {
	seen := map[string]struct{}{}
	var versions []string
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		versions = append(versions, v)
	}
	// Numeric-aware descending sort: a lexicographic sort would rank
	// "0.2.9" above "0.2.14".
	sort.Slice(versions, func(i, j int) bool {
		return Less(versions[j], versions[i])
	})
	// Surface "latest" first if present.
	for i, v := range versions {
		if v == "latest" {
			versions = append([]string{v}, append(versions[:i:i], versions[i+1:]...)...)
			break
		}
	}
	return versions
}

// Less compares agent version strings, delegating to domain/node so the CDN
// listing's order and the fleet's "update available" answer cannot disagree.
// The implementation and the caveats are there.
func Less(a, b string) bool {
	return node.AgentVersionLess(a, b)
}
