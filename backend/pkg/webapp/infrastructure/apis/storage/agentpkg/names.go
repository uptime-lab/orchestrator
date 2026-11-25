package agentpkg

import (
	"fmt"
	"path"
	"strings"
)

// Package base names the agent CDN serves. The full agent is listed by
// GET /cdn/agent-versions; the watcher shares the shelf and the naming but is
// deliberately absent from that listing (see Versions).
const (
	AgentPackage   = "orchestrator-agent"
	WatcherPackage = "orchestrator-watcher"
)

// LatestVersion is the literal version string of the `latest` alias. It is a
// file name, never resolved to the highest version.
const LatestVersion = "latest"

// SignatureSuffix is appended to a package's file name for its detached
// signature.
const SignatureSuffix = ".sig"

// FileName is the flat CDN file name of a package:
// <pkg>_<version>_linux_<arch>.<ext>. The CDN handler and the release import
// both build names here, so what one writes is what the other reads.
func FileName(pkg, version, arch, ext string) string {
	return fmt.Sprintf("%s_%s_linux_%s.%s", pkg, version, arch, ext)
}

// ObjectKey joins the configured agent CDN base path (Storage.AgentCDNBasePath)
// and a file name into the storage key, trimming the base path's slashes the
// way the CDN handler always has.
func ObjectKey(basePath, fileName string) string {
	return path.Join(strings.Trim(basePath, "/"), fileName)
}
