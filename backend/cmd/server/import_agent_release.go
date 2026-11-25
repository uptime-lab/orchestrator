package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/agentpkg"
	local_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/local"
	s3_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/s3"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/signing"
)

// importAgentReleaseFlag is the second one-shot form of the server binary
// (after --init-secrets): it imports a signed agent release — the `.deb`s and
// their detached `.sig`s from the agent repository's GitHub Release — into
// this install's storage, under the keys the /cdn/agent and /cdn/watcher
// routes read, so update_agent, the install link and /cdn/agent-versions
// serve it. It is a flag on THIS binary because the published image carries
// only /app/server, and because it must make the same storage decision the
// server makes (storageBackendKind) or it would publish into a store the CDN
// never reads.
//
// The input is LOCAL files only. The server never reaches out to fetch a
// release: the operator downloads the assets (browser, `gh release
// download`) and mounts them into the container. A URL fetch would add an
// egress path, redirect handling and a second bounded-download
// implementation for no capability the operator lacks.
//
// Everything is verified before anything is written: each package's
// signature against the trusted keys embedded in this binary (the same set
// install.sh is baked with), its control file against its name, and any
// checksum file present. Nothing is ever deleted: the CDN keeps `latest`, the
// current release and one rollback step, and pruning is the operator's call.
const importAgentReleaseFlag = "--import-agent-release"

const (
	flagSetLatest = "--set-latest"
	flagReplace   = "--replace"
)

// Bounds. maxImportPackageBytes is the agent's own maxAgentPackageBytes: a
// package larger than that is one no device would install, so importing it
// would only publish a guaranteed failure. A DER ECDSA P-384 signature is at
// most 104 bytes; the slack covers nothing legitimate and refuses a wrong
// file early.
const (
	maxImportPackageBytes  = 128 << 20
	maxImportSignatureByte = 1 << 10
	maxImportChecksumBytes = 1 << 20
	maxDebControlBytes     = 1 << 20
	maxDebControlFileBytes = 64 << 10
	importTimeout          = 30 * time.Minute
)

var (
	// The package names a release carries. Version as the CDN accepts it
	// (cdn.semverRegex without the `v`, which the name may carry and the key
	// never does — a release tag's `v` is stripped because a Debian Version
	// starts with a digit). `latest` is not a version: it is written only by --set-latest.
	importPkgRe = regexp.MustCompile(`^(orchestrator-agent|orchestrator-watcher)_v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)_linux_(arm64|amd64)\.deb$`)
	// Names that are package-shaped but not importable: refused rather than
	// ignored, because a `_latest_` file or a renamed package is exactly what
	// an operator must not believe was published.
	debLikeRe = regexp.MustCompile(`\.deb(\.sig|\.sha256)?$`)
	// Combined checksum files (sha256sum format).
	checksumFileRe = regexp.MustCompile(`(?i)^(sha256sums(\.txt)?|checksums\.txt|.*_checksums\.txt)$`)
	sbomRe         = regexp.MustCompile(`\.(spdx|cdx|sbom)\.json$`)
	sha256HexRe    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// importOptions is the parsed command line.
type importOptions struct {
	paths     []string
	setLatest bool
	replace   bool
}

// errImportUsage marks a command-line mistake (exit 2), as opposed to a
// refusal or a failure (exit 1).
var errImportUsage = errors.New("usage")

func importUsage(w io.Writer) {
	fmt.Fprintf(w, "usage: server %s [%s] [%s] DIR|FILE...\n", importAgentReleaseFlag, flagSetLatest, flagReplace)
	fmt.Fprintln(w, "  DIR|FILE  the release assets: orchestrator-agent_<v>_linux_<arch>.deb and")
	fmt.Fprintln(w, "            orchestrator-watcher_<v>_linux_<arch>.deb, each with its .deb.sig beside it")
	fmt.Fprintf(w, "  %s  also point `latest` at this version and record it in %s per architecture (default: import only).\n", flagSetLatest, agentpkg.LatestMarkerNameFor("<arch>"))
	fmt.Fprintln(w, "              One-click agent updates install `latest`, and the fleet's \"update available\"")
	fmt.Fprintln(w, "              compares against it: without this flag they keep installing the previous release.")
	fmt.Fprintln(w, "              Only the architectures this release carries are moved.")
	fmt.Fprintf(w, "  %s     overwrite this version's objects if they already exist; on a failure the\n", flagReplace)
	fmt.Fprintln(w, "              previous objects are put back")
}

// parseImportArgs reports whether the arguments ask for the import and
// parses them. The flag is accepted anywhere, like --init-secrets; its
// `=PATH` spelling names the first path.
func parseImportArgs(args []string) (opts importOptions, present bool, err error) {
	idx := -1
	for i, a := range args {
		if a == importAgentReleaseFlag || strings.HasPrefix(a, importAgentReleaseFlag+"=") {
			idx = i
			break
		}
	}
	if idx < 0 {
		return opts, false, nil
	}
	if v, ok := strings.CutPrefix(args[idx], importAgentReleaseFlag+"="); ok {
		if strings.TrimSpace(v) == "" {
			return opts, true, fmt.Errorf("%w: empty path", errImportUsage)
		}
		opts.paths = append(opts.paths, v)
	}
	for i, a := range args {
		if i == idx {
			continue
		}
		switch {
		case a == flagSetLatest:
			opts.setLatest = true
		case a == flagReplace:
			opts.replace = true
		case strings.HasPrefix(a, "-"):
			return opts, true, fmt.Errorf("%w: unknown option %s", errImportUsage, a)
		default:
			opts.paths = append(opts.paths, a)
		}
	}
	if len(opts.paths) == 0 {
		return opts, true, fmt.Errorf("%w: no release files named", errImportUsage)
	}
	return opts, true, nil
}

// importAgentReleaseCommand handles the flag: 0 imported, 1 refused or
// failed, 2 usage — the same codes as --init-secrets.
func importAgentReleaseCommand(args []string, stdout, stderr io.Writer) (handled bool, code int) {
	opts, present, err := parseImportArgs(args)
	if !present {
		return false, 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "import-agent-release: %v\n", err)
		importUsage(stderr)
		return true, 2
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig(logger)
	if err != nil {
		fmt.Fprintf(stderr, "import-agent-release: load configuration: %v\n", err)
		return true, 1
	}
	kind := storageBackendKind(cfg)
	if !storage.ServedThroughCDN(kind) {
		fmt.Fprintf(stderr, "import-agent-release: the configured storage backend (%s) is not served through /cdn, so no device could fetch an imported package; nothing was written\n", kind)
		return true, 1
	}
	var store storage.StorageClient
	switch kind {
	case storage.TypeS3:
		c, err := s3_storage.NewS3StorageClient(s3ConfigFrom(cfg), logger)
		if err != nil {
			fmt.Fprintf(stderr, "import-agent-release: open object storage: %v\n", err)
			return true, 1
		}
		store = c
	default:
		c, err := local_storage.NewLocalStorageClient(cfg.Storage.Local.BaseDir, cfg.Storage.Local.BaseURL)
		if err != nil {
			fmt.Fprintf(stderr, "import-agent-release: open local storage: %v\n", err)
			return true, 1
		}
		store = c
	}
	keys, err := signing.TrustedKeys()
	if err != nil {
		fmt.Fprintf(stderr, "import-agent-release: trusted keys: %v\n", err)
		return true, 1
	}
	logger.Info("importing agent release", "storage", kind, "base_path", cfg.Storage.AgentCDNBasePath, "trusted_keys", len(keys))

	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	defer cancel()
	im := &releaseImporter{
		store:    store,
		basePath: cfg.Storage.AgentCDNBasePath,
		keys:     keys,
		stdout:   stdout,
		logger:   logger,
	}
	if err := im.run(ctx, opts); err != nil {
		fmt.Fprintf(stderr, "import-agent-release: %v\n", err)
		return true, 1
	}
	return true, 0
}

// releasePackage is one package of the release, from file name to the
// verified private copy that is what gets stored.
type releasePackage struct {
	pkg, version, arch string
	debPath, sigPath   string

	// Filled by verify.
	copyPath string // private copy the bytes were verified from
	size     int64
	sig      []byte
	signer   signing.TrustedKey
}

func (p *releasePackage) label() string {
	return p.pkg + " " + p.version + " " + p.arch
}

func (p *releasePackage) fileName(version string) string {
	return agentpkg.FileName(p.pkg, version, p.arch, "deb")
}

// releaseImporter does the work; the command wires it to the configured
// storage and the embedded keys, tests to a local store and a throwaway key.
type releaseImporter struct {
	store    storage.StorageClient
	basePath string
	keys     []signing.TrustedKey
	stdout   io.Writer
	logger   *slog.Logger
	// workDir holds the private copies; empty means a fresh temp dir under
	// TMPDIR (on the community compose, the storage volume's tmp/).
	workDir string
}

func (im *releaseImporter) run(ctx context.Context, opts importOptions) error {
	pkgs, sums, err := collectRelease(opts.paths, im.logger)
	if err != nil {
		return err
	}

	work := im.workDir
	if work == "" {
		work, err = os.MkdirTemp("", "agent-release-import-*")
		if err != nil {
			return fmt.Errorf("create a private work directory: %w", err)
		}
		defer func() { _ = os.RemoveAll(work) }()
	}

	// 1. Verify EVERYTHING before writing anything.
	for i, p := range pkgs {
		if err := im.verify(p, filepath.Join(work, fmt.Sprintf("pkg-%d.deb", i)), sums); err != nil {
			return fmt.Errorf("%s: %w; nothing was written", filepath.Base(p.debPath), err)
		}
		im.logger.Info("verified", "package", p.label(), "bytes", p.size, "key", p.signer.ID())
	}

	// 2. Refuse to overwrite a published version unless asked.
	if !opts.replace {
		var existing []string
		for _, p := range pkgs {
			for _, name := range []string{p.fileName(p.version), p.fileName(p.version) + agentpkg.SignatureSuffix} {
				key := agentpkg.ObjectKey(im.basePath, name)
				ok, err := im.store.Exists(ctx, key)
				if err != nil {
					return fmt.Errorf("check %s: %w; nothing was written", key, err)
				}
				if ok {
					existing = append(existing, key)
				}
			}
		}
		if len(existing) > 0 {
			return fmt.Errorf("already published: %s; pass %s to overwrite them. Nothing was written", strings.Join(existing, ", "), flagReplace)
		}
	}

	// 3. The versioned objects. Signature first: /cdn/agent-versions lists a
	// version by its .deb, so a version never appears without its .sig.
	//
	// Under --replace the version is ALREADY published and devices may be
	// fetching it, so the objects being overwritten are saved first and any
	// failure puts them back — the same all-or-nothing `latest` gets. Without
	// it a failure part-way left the new .sig beside the old .deb (or the
	// reverse): a published version no device could verify.
	if err := im.writeVersioned(ctx, pkgs, work, opts.replace); err != nil {
		return err
	}

	if !opts.setLatest {
		im.printLatestUnchanged(ctx, pkgs, work)
		return nil
	}
	if err := im.moveLatest(ctx, pkgs, work); err != nil {
		return err
	}
	im.printArchesLeftBehind(ctx, pkgs, work)
	return nil
}

// restoreTimeout bounds putting saved objects back. It runs under a FRESH
// context: the import's own deadline is the commonest reason a write fails,
// and a restore under that same, already expired context failed every time.
const restoreTimeout = 10 * time.Minute

func restoreContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
}

// savedObject is one object an import is about to overwrite.
type savedObject struct {
	key     string
	path    string // "" = the object did not exist before
	written bool
}

// putBack restores every written object of plan from its saved copy and
// returns the keys that could not be put back as they were.
func (im *releaseImporter) putBack(ctx context.Context, plan []*savedObject) []string {
	rctx, cancel := restoreContext(ctx)
	defer cancel()
	var left []string
	for _, r := range plan {
		if !r.written {
			continue
		}
		if r.path == "" {
			// There was no previous object to put back, and this command
			// deletes nothing. Name it only if the write actually left one.
			if exists, xerr := im.store.Exists(rctx, r.key); xerr != nil || exists {
				left = append(left, r.key+" (did not exist before this import)")
			}
			continue
		}
		if rerr := im.restore(rctx, r.key, r.path); rerr != nil {
			left = append(left, r.key+" (restore failed: "+rerr.Error()+")")
		}
	}
	return left
}

// writeVersioned stores every package's versioned .sig and .deb.
func (im *releaseImporter) writeVersioned(ctx context.Context, pkgs []*releasePackage, work string, replace bool) error {
	type step struct {
		p     *releasePackage
		src   string
		saved *savedObject
	}
	var steps []step
	var plan []*savedObject
	for i, p := range pkgs {
		for j, src := range []string{"", p.copyPath} {
			name := p.fileName(p.version)
			if src == "" {
				name += agentpkg.SignatureSuffix
			}
			s := &savedObject{key: agentpkg.ObjectKey(im.basePath, name)}
			if replace {
				prev, err := im.snapshot(ctx, s.key, filepath.Join(work, fmt.Sprintf("versioned-%d-%d", i, j)))
				if err != nil {
					return fmt.Errorf("save the current %s before replacing it: %w; nothing was written", s.key, err)
				}
				s.path = prev
			}
			plan = append(plan, s)
			steps = append(steps, step{p: p, src: src, saved: s})
		}
	}
	for _, st := range steps {
		n, err := im.put(ctx, st.saved.key, st.p, st.src)
		st.saved.written = true // a failed write may still have replaced the object
		if err != nil {
			if !replace {
				return fmt.Errorf("store %s: %w. `latest` was not touched; re-run with %s once storage is healthy", st.saved.key, err, flagReplace)
			}
			im.logger.Error("replacing a published version failed; restoring its previous objects", "key", st.saved.key, "error", err)
			if left := im.putBack(ctx, plan); len(left) > 0 {
				return fmt.Errorf("store %s: %w. `latest` was not touched, but these objects of the published version could not be put back, so its package and signature may not match: %s. Re-run with %s once storage is healthy",
					st.saved.key, err, strings.Join(left, ", "), flagReplace)
			}
			return fmt.Errorf("store %s: %w. The published version is as it was and `latest` was not touched; re-run with %s once storage is healthy", st.saved.key, err, flagReplace)
		}
		im.printStored(st.p, st.p.version, st.saved.key, n)
	}
	return nil
}

// latestVersionFor reads which version an arch's `latest` agent package is,
// from the package's own control file; "" when there is no `latest` for it or
// it cannot be read.
func (im *releaseImporter) latestVersionFor(ctx context.Context, arch, work string) string {
	key := agentpkg.ObjectKey(im.basePath, agentpkg.FileName(agentpkg.AgentPackage, agentpkg.LatestVersion, arch, "deb"))
	dst := filepath.Join(work, "current-latest-"+arch+".deb")
	_ = os.Remove(dst)
	path, err := im.snapshot(ctx, key, dst)
	if err != nil || path == "" {
		return ""
	}
	defer func() { _ = os.Remove(path) }()
	_, v, _, err := readDebControl(path)
	if err != nil {
		return ""
	}
	return debVersionToName(v)
}

// printLatestUnchanged says, after an import that did not move `latest`, what
// a one-click update installs: it dispatches `latest`, not the version just
// imported, so an operator who expected the fleet to move must hear it.
func (im *releaseImporter) printLatestUnchanged(ctx context.Context, pkgs []*releasePackage, work string) {
	version := pkgs[0].version
	fmt.Fprintf(im.stdout, "latest unchanged: %s is published but `latest` was not moved (pass %s to point it at %s)\n", version, flagSetLatest, version)
	for _, arch := range releaseArches(pkgs) {
		if cur := im.latestVersionFor(ctx, arch, work); cur != "" {
			fmt.Fprintf(im.stdout, "  one-click agent updates on %s nodes keep installing %s\n", arch, cur)
		} else {
			fmt.Fprintf(im.stdout, "  no `latest` is published for %s: one-click agent updates on %s nodes will fail until %s is used\n", arch, arch, flagSetLatest)
		}
	}
}

// knownArches are the architectures the CDN serves.
var knownArches = []string{"amd64", "arm64"}

// printArchesLeftBehind names, after --set-latest, every architecture whose
// `latest` the release did NOT move because it carries no package for it.
func (im *releaseImporter) printArchesLeftBehind(ctx context.Context, pkgs []*releasePackage, work string) {
	in := map[string]bool{}
	for _, a := range releaseArches(pkgs) {
		in[a] = true
	}
	for _, arch := range knownArches {
		if in[arch] {
			continue
		}
		if cur := im.latestVersionFor(ctx, arch, work); cur != "" {
			fmt.Fprintf(im.stdout, "warning: latest for %s still installs %s; this release carries no %s package, so %s nodes are not moved to %s\n",
				arch, cur, arch, arch, pkgs[0].version)
		}
	}
}

// coversEveryKnownArch reports whether the release carries an agent for every
// architecture the CDN serves.
func coversEveryKnownArch(pkgs []*releasePackage) bool {
	in := map[string]bool{}
	for _, a := range releaseArches(pkgs) {
		in[a] = true
	}
	for _, arch := range knownArches {
		if !in[arch] {
			return false
		}
	}
	return true
}

// releaseArches lists the architectures the release carries an agent for.
func releaseArches(pkgs []*releasePackage) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.pkg == agentpkg.AgentPackage && !seen[p.arch] {
			seen[p.arch] = true
			out = append(out, p.arch)
		}
	}
	sort.Strings(out)
	return out
}

// debVersionToName maps a Debian control Version to the version the file name
// and the CDN key carry. A pre-release is `0.8.0~rc.1` inside the package
// (`~` sorts before the release in Debian) and `0.8.0-rc.1` in the name.
func debVersionToName(v string) string {
	return strings.ReplaceAll(v, "~", "-")
}

// moveLatest writes the `latest` objects of every package, and then the
// markers recording which version `latest` now is per architecture
// (agentpkg.LatestMarkerNameFor),
// as a SEPARATE step after every versioned object is in place. It is
// all-or-nothing as far as storage allows: the previous `latest` objects and
// marker are saved first, and any failure writes them back, so a half-moved
// `latest` (a new .sig beside an old .deb, the agent moved and the watcher
// not, or a marker naming a version `latest` is not) is not left behind.
func (im *releaseImporter) moveLatest(ctx context.Context, pkgs []*releasePackage, work string) error {
	type step struct {
		p     *releasePackage // nil = the marker
		src   string
		saved *savedObject
	}
	var plan []*savedObject
	var steps []step
	for i, p := range pkgs {
		for j, src := range []string{"", p.copyPath} {
			name := p.fileName(agentpkg.LatestVersion)
			if src == "" {
				name += agentpkg.SignatureSuffix
			}
			s := &savedObject{key: agentpkg.ObjectKey(im.basePath, name)}
			prev, err := im.snapshot(ctx, s.key, filepath.Join(work, fmt.Sprintf("latest-%d-%d", i, j)))
			if err != nil {
				return fmt.Errorf("save the current %s before moving it: %w; `latest` was not touched", s.key, err)
			}
			s.path = prev
			if prev != "" && src != "" {
				if _, v, _, err := readDebControl(prev); err == nil {
					v = debVersionToName(v)
					if agentpkg.Less(p.version, v) {
						im.logger.Warn("latest moves BACKWARDS", "package", p.pkg, "from", v, "to", p.version)
					} else {
						im.logger.Info("latest moves", "package", p.pkg, "from", v, "to", p.version)
					}
				}
			}
			plan = append(plan, s)
			steps = append(steps, step{p: p, src: src, saved: s})
		}
	}
	// One marker per architecture the release carries, `latest.<arch>.version`:
	// an architecture this release has no package for keeps its `latest` and
	// its marker. The legacy
	// architecture-agnostic marker is written only when the release moves
	// every architecture the CDN serves, because only then is one version
	// true for all of them.
	markerKeys := []string{}
	for _, arch := range releaseArches(pkgs) {
		markerKeys = append(markerKeys, agentpkg.ObjectKey(im.basePath, agentpkg.LatestMarkerNameFor(arch)))
	}
	if coversEveryKnownArch(pkgs) {
		markerKeys = append(markerKeys, agentpkg.ObjectKey(im.basePath, agentpkg.LatestMarkerName))
	}
	for i, key := range markerKeys {
		marker := &savedObject{key: key}
		prev, err := im.snapshot(ctx, marker.key, filepath.Join(work, fmt.Sprintf("latest-marker-%d", i)))
		if err != nil {
			return fmt.Errorf("save the current %s before moving it: %w; `latest` was not touched", marker.key, err)
		}
		marker.path = prev
		plan = append(plan, marker)
		steps = append(steps, step{saved: marker})
	}

	version := pkgs[0].version
	for _, st := range steps {
		var n int64
		var err error
		if st.p == nil {
			n, err = im.putBytes(ctx, st.saved.key, []byte(version+"\n"), "text/plain")
		} else {
			n, err = im.put(ctx, st.saved.key, st.p, st.src)
		}
		st.saved.written = true // a failed write may still have replaced the object
		if err != nil {
			im.logger.Error("moving latest failed; restoring the previous latest objects", "key", st.saved.key, "error", err)
			if left := im.putBack(ctx, plan); len(left) > 0 {
				return fmt.Errorf("store %s: %w. The versioned objects are in place; these latest objects could not be put back as they were and must be checked by hand: %s", st.saved.key, err, strings.Join(left, ", "))
			}
			return fmt.Errorf("store %s: %w. The versioned objects are in place and `latest` is as it was; re-run with %s %s", st.saved.key, err, flagReplace, flagSetLatest)
		}
		if st.p == nil {
			fmt.Fprintf(im.stdout, "stored  %-20s %-10s %-6s %10d bytes  %s\n", "latest marker", version, "", n, st.saved.key)
		} else {
			im.printStored(st.p, agentpkg.LatestVersion, st.saved.key, n)
		}
	}
	return nil
}

// put stores one object: the signature when src is "", else the package
// copy. The size is read back, so a short write is a failure here rather
// than a signature mismatch on every device.
func (im *releaseImporter) put(ctx context.Context, key string, p *releasePackage, src string) (int64, error) {
	var (
		body        io.Reader
		want        int64
		contentType string
	)
	if src == "" {
		body, want, contentType = bytes.NewReader(p.sig), int64(len(p.sig)), "application/octet-stream"
	} else {
		f, err := os.Open(src)
		if err != nil {
			return 0, err
		}
		defer func() { _ = f.Close() }()
		body, want, contentType = f, p.size, "application/vnd.debian.binary-package"
	}
	return im.putReader(ctx, key, body, want, contentType)
}

// putBytes stores a small object from memory, read back like put.
func (im *releaseImporter) putBytes(ctx context.Context, key string, data []byte, contentType string) (int64, error) {
	return im.putReader(ctx, key, bytes.NewReader(data), int64(len(data)), contentType)
}

func (im *releaseImporter) putReader(ctx context.Context, key string, body io.Reader, want int64, contentType string) (int64, error) {
	if _, err := im.store.StoreWithOptions(ctx, key, body, &storage.StorageOptions{ContentType: contentType}); err != nil {
		return 0, err
	}
	info, err := im.store.GetFileInfo(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("read back: %w", err)
	}
	if info == nil || info.Size != want {
		return 0, fmt.Errorf("read back: stored size does not match the %d bytes written", want)
	}
	return want, nil
}

// snapshot copies an existing object into dst (bounded) and returns dst, or
// "" when the object does not exist.
func (im *releaseImporter) snapshot(ctx context.Context, key, dst string) (string, error) {
	info, err := im.store.GetFileInfo(ctx, key)
	if err != nil {
		return "", err
	}
	if info == nil {
		return "", nil
	}
	r, err := im.store.Retrieve(ctx, key)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := copyCapped(f, r, maxImportPackageBytes); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return dst, nil
}

func (im *releaseImporter) restore(ctx context.Context, key, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = im.store.StoreWithOptions(ctx, key, f, &storage.StorageOptions{ContentType: "application/octet-stream"})
	return err
}

func (im *releaseImporter) printStored(p *releasePackage, version, key string, n int64) {
	fmt.Fprintf(im.stdout, "stored  %-20s %-10s %-6s %10d bytes  signed by %s  %s\n",
		p.pkg, version, p.arch, n, p.signer.ID(), key)
}

// verify copies the package once into a private file, hashing as it goes,
// and checks the signature, the checksums and the control file against THAT
// copy — the bytes stored later are the bytes verified, whatever happens to
// the operator's directory in between.
func (im *releaseImporter) verify(p *releasePackage, dst string, sums checksums) error {
	sig, err := readCapped(p.sigPath, maxImportSignatureByte)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no detached signature %s beside the package", filepath.Base(p.sigPath))
		}
		return fmt.Errorf("signature: %w", err)
	}
	p.sig = sig

	src, err := os.Open(p.debPath)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("private copy: %w", err)
	}
	h384, h256 := sha512.New384(), sha256.New()
	n, err := copyCapped(io.MultiWriter(out, h384, h256), src, maxImportPackageBytes)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	p.copyPath, p.size = dst, n

	var digest [sha512.Size384]byte
	copy(digest[:], h384.Sum(nil))
	signer, err := signing.VerifyDigest(im.keys, digest, sig)
	if err != nil {
		return fmt.Errorf("signature refused: %w", err)
	}
	p.signer = signer

	if err := sums.check(filepath.Base(p.debPath), hex.EncodeToString(h256.Sum(nil))); err != nil {
		return err
	}
	sigSum := sha256.Sum256(sig)
	if err := sums.check(filepath.Base(p.sigPath), hex.EncodeToString(sigSum[:])); err != nil {
		return err
	}

	// The signature covers bytes, not the file name: a validly signed older
	// package renamed to a newer version, or an agent renamed as the watcher,
	// would verify. The control file inside the package is signed, so the
	// name must agree with it.
	pkgName, version, arch, err := readDebControl(dst)
	if err != nil {
		return fmt.Errorf("read the package's control file: %w", err)
	}
	// A pre-release is `0.8.0~rc.1` in the control file and `-rc.1` in the
	// name (nfpm), so the control version is compared in the name's spelling.
	if pkgName != p.pkg || debVersionToName(version) != p.version || arch != p.arch {
		return fmt.Errorf("the file name says %s %s %s but the signed package says %s %s %s", p.pkg, p.version, p.arch, pkgName, version, arch)
	}
	return nil
}

// collectRelease turns the named paths into packages, each paired with the
// signature beside it, plus whatever checksums were supplied.
func collectRelease(paths []string, logger *slog.Logger) ([]*releasePackage, checksums, error) {
	type entry struct {
		path     string
		explicit bool
	}
	var files []entry
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, nil, err
		}
		if !st.IsDir() {
			files = append(files, entry{p, true})
			continue
		}
		des, err := os.ReadDir(p)
		if err != nil {
			return nil, nil, err
		}
		for _, de := range des {
			files = append(files, entry{filepath.Join(p, de.Name()), false})
		}
	}

	sums := checksums{}
	byKey := map[string]*releasePackage{}
	for _, f := range files {
		base := filepath.Base(f.path)
		st, err := os.Stat(f.path)
		if err != nil {
			return nil, nil, err
		}
		if !st.Mode().IsRegular() {
			if f.explicit {
				return nil, nil, fmt.Errorf("%s is not a regular file", f.path)
			}
			continue
		}
		switch {
		case importPkgRe.MatchString(base):
			m := importPkgRe.FindStringSubmatch(base)
			p := &releasePackage{pkg: m[1], version: m[2], arch: m[3], debPath: f.path, sigPath: f.path + agentpkg.SignatureSuffix}
			k := p.label()
			if prev, dup := byKey[k]; dup {
				return nil, nil, fmt.Errorf("%s is named twice (%s and %s)", k, prev.debPath, f.path)
			}
			byKey[k] = p
		case strings.HasSuffix(base, ".deb"+agentpkg.SignatureSuffix) && importPkgRe.MatchString(strings.TrimSuffix(base, agentpkg.SignatureSuffix)):
			// Paired with its package below.
		case strings.HasSuffix(base, ".sha256") && (importPkgRe.MatchString(strings.TrimSuffix(base, ".sha256")) ||
			importPkgRe.MatchString(strings.TrimSuffix(strings.TrimSuffix(base, ".sha256"), agentpkg.SignatureSuffix))):
			if err := sums.addSingle(f.path, strings.TrimSuffix(base, ".sha256")); err != nil {
				return nil, nil, err
			}
		case debLikeRe.MatchString(base):
			return nil, nil, fmt.Errorf("%s: not an importable name; expected orchestrator-agent_<version>_linux_<arch>.deb or orchestrator-watcher_<version>_linux_<arch>.deb (a `latest` file is never imported: --set-latest writes it)", base)
		case checksumFileRe.MatchString(base):
			if err := sums.addCombined(f.path); err != nil {
				return nil, nil, err
			}
		case sbomRe.MatchString(base):
			logger.Info("ignoring the SBOM; it is not served by the CDN", "file", base)
		default:
			if f.explicit {
				return nil, nil, fmt.Errorf("%s: not part of an agent release", base)
			}
			logger.Info("ignoring a file that is not part of an agent release", "file", base)
		}
	}

	if len(byKey) == 0 {
		return nil, nil, errors.New("no agent package found; expected orchestrator-agent_<version>_linux_<arch>.deb with its .deb.sig")
	}
	pkgs := make([]*releasePackage, 0, len(byKey))
	for _, p := range byKey {
		pkgs = append(pkgs, p)
	}
	// Agent before watcher, then by arch: a stable order for the summary.
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].label() < pkgs[j].label() })

	version := pkgs[0].version
	hasAgent := map[string]bool{}
	hasWatcher := map[string]bool{}
	for _, p := range pkgs {
		if p.version != version {
			return nil, nil, fmt.Errorf("one release at a time: found versions %s and %s", version, p.version)
		}
		if p.pkg == agentpkg.AgentPackage {
			hasAgent[p.arch] = true
		} else {
			hasWatcher[p.arch] = true
		}
	}
	if len(hasAgent) == 0 {
		return nil, nil, fmt.Errorf("the release carries no %s package; the watcher alone cannot be imported", agentpkg.AgentPackage)
	}
	for arch := range hasAgent {
		if !hasWatcher[arch] {
			logger.Warn("no watcher package for this architecture; installing Watcher on such a node will find nothing", "arch", arch, "version", version)
		}
	}
	for arch := range hasWatcher {
		if !hasAgent[arch] {
			return nil, nil, fmt.Errorf("a watcher package for %s without the agent package for %s", arch, arch)
		}
	}
	return pkgs, sums, nil
}

// checksums maps a file's base name to its expected SHA-256 (lowercase hex).
type checksums map[string]string

func (c checksums) add(name, sum string) error {
	sum = strings.ToLower(sum)
	if prev, ok := c[name]; ok && prev != sum {
		return fmt.Errorf("two different checksums are given for %s", name)
	}
	c[name] = sum
	return nil
}

// addSingle reads a <file>.sha256: the hex alone, or one sha256sum line.
func (c checksums) addSingle(path, name string) error {
	b, err := readCapped(path, maxImportChecksumBytes)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 || !sha256HexRe.MatchString(fields[0]) {
		return fmt.Errorf("%s: not a SHA-256 checksum file", filepath.Base(path))
	}
	return c.add(name, fields[0])
}

// addCombined reads a sha256sum-format list ("<hex>  <name>" or
// "<hex> *<name>"). Names are compared by base name.
func (c checksums) addCombined(path string) error {
	b, err := readCapped(path, maxImportChecksumBytes)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !sha256HexRe.MatchString(fields[0]) {
			return fmt.Errorf("%s: unreadable line %q", filepath.Base(path), line)
		}
		if err := c.add(filepath.Base(strings.TrimPrefix(fields[1], "*")), fields[0]); err != nil {
			return err
		}
	}
	return sc.Err()
}

// check compares a computed digest with a supplied one; a file no checksum
// names passes — the signature is the authenticity check, a checksum only
// catches a damaged download early.
func (c checksums) check(name, got string) error {
	want, ok := c[name]
	if !ok {
		return nil
	}
	if want != got {
		return fmt.Errorf("checksum mismatch for %s: the checksum file says %s, the file is %s", name, want, got)
	}
	return nil
}

// copyCapped copies at most limit bytes and fails on byte limit+1, so an
// oversized input is refused rather than silently truncated (the agent's
// copyDownloadBounded rule).
func copyCapped(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("larger than the %d-byte limit", limit)
	}
	return n, nil
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	if _, err := copyCapped(&buf, f, limit); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return buf.Bytes(), nil
}

// readDebControl returns Package, Version and Architecture from a .deb's
// control file. A .deb is an ar archive whose control.tar member holds
// ./control. Only gzip-compressed and uncompressed control members are read
// — what the release's packager (nfpm) writes; anything else is refused
// rather than imported unchecked.
func readDebControl(path string) (pkg, version, arch string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", "", err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "!<arch>\n" {
		return "", "", "", errors.New("not a Debian package (no ar header)")
	}
	hdr := make([]byte, 60)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			return "", "", "", errors.New("no control member")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(hdr[0:16])), "/")
		size, perr := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if perr != nil || size < 0 || string(hdr[58:60]) != "`\n" {
			return "", "", "", errors.New("malformed ar member header")
		}
		if !strings.HasPrefix(name, "control.tar") {
			if _, err := io.CopyN(io.Discard, r, size+size%2); err != nil {
				return "", "", "", errors.New("truncated ar archive")
			}
			continue
		}
		if size > maxDebControlBytes {
			return "", "", "", errors.New("control member too large")
		}
		member := io.LimitReader(r, size)
		var tr *tar.Reader
		switch name {
		case "control.tar.gz":
			gz, err := gzip.NewReader(member)
			if err != nil {
				return "", "", "", fmt.Errorf("control.tar.gz: %w", err)
			}
			defer func() { _ = gz.Close() }()
			tr = tar.NewReader(io.LimitReader(gz, maxDebControlBytes))
		case "control.tar":
			tr = tar.NewReader(member)
		default:
			return "", "", "", fmt.Errorf("control member %s uses a compression this importer does not read", name)
		}
		for {
			th, err := tr.Next()
			if err != nil {
				return "", "", "", errors.New("no control file in the control member")
			}
			if strings.TrimPrefix(th.Name, "./") != "control" {
				continue
			}
			var buf bytes.Buffer
			if _, err := copyCapped(&buf, tr, maxDebControlFileBytes); err != nil {
				return "", "", "", err
			}
			fields := map[string]string{}
			for _, line := range strings.Split(buf.String(), "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok || strings.HasPrefix(line, " ") {
					continue
				}
				k = strings.TrimSpace(k)
				if _, seen := fields[k]; !seen {
					fields[k] = strings.TrimSpace(v)
				}
			}
			return fields["Package"], fields["Version"], fields["Architecture"], nil
		}
	}
}
