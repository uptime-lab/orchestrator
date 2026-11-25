package compression

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/compression"
)

// XZCLICompressor compresses streams by piping them through the system xz
// binary. The Go xz implementations are single-threaded and an order of
// magnitude slower, which matters for multi-GB OS images on ARM hosts.
type XZCLICompressor struct {
	binary string
	// memLimitMB bounds what one xz process may allocate, for compression and
	// for multi-threaded decompression alike. `-T0` sizes threads and
	// dictionaries from the machine's PHYSICAL RAM, not the container's cgroup
	// limit, so inside a memory-limited container an unbounded run is an OOM kill of
	// the whole backend rather than a slow job.
	memLimitMB int
}

var _ compression.Compressor = (*XZCLICompressor)(nil)

// NewXZCLICompressor returns a compressor backed by the system xz binary with
// the default memory bound, or an error if xz is not installed.
func NewXZCLICompressor() (*XZCLICompressor, error) {
	return NewXZCLICompressorWithMemLimit(config.DefaultImageXZMemLimitMB)
}

// NewXZCLICompressorWithMemLimit is NewXZCLICompressor with an explicit bound
// in MiB (config.ImageConfig.EffectiveXZMemLimitMB); 0 and negatives mean the
// default, never unlimited.
func NewXZCLICompressorWithMemLimit(memLimitMB int) (*XZCLICompressor, error) {
	binary, err := exec.LookPath("xz")
	if err != nil {
		return nil, fmt.Errorf("xz binary not found in PATH: %w", err)
	}
	if memLimitMB <= 0 {
		memLimitMB = config.DefaultImageXZMemLimitMB
	}
	return &XZCLICompressor{binary: binary, memLimitMB: memLimitMB}, nil
}

// compressArgs is `xz -T0 -6 -c` under a memory bound: xz reduces the thread
// count first and the dictionary last to fit, so the archive stays valid and
// only the speed changes. --no-warn keeps an adjustment from ever becoming
// exit status 2: on 5.8 the adjustment is printed as a notice (measured, exit
// 0), but xz's contract is that any warning-class condition sets status 2,
// which exec.Run reports as an error — so a constrained run must never
// depend on which class a given release files the message under.
func (c *XZCLICompressor) compressArgs() []string {
	return []string{"-T0", "-6", "-c", "--no-warn", fmt.Sprintf("--memlimit-compress=%dMiB", c.memLimitMB)}
}

// decompressArgs is `xz -dc -T0` under two bounds: the multi-threaded limit
// is soft (xz drops to fewer threads, then to one), the plain decompress
// limit is HARD — an archive whose dictionary alone needs more than the bound
// is refused with an xz error rather than decoded, which is the graceful
// answer to a crafted stream. Both need xz >= 5.4 (the backend image's
// Alpine ships 5.8).
func (c *XZCLICompressor) decompressArgs() []string {
	return []string{
		"-dc", "-T0", "--no-warn",
		fmt.Sprintf("--memlimit-mt-decompress=%dMiB", c.memLimitMB),
		fmt.Sprintf("--memlimit-decompress=%dMiB", c.memLimitMB),
	}
}

// Compress runs `xz -T0 -6 -c` under the memory bound, stdin to stdout.
func (c *XZCLICompressor) Compress(ctx context.Context, dst io.Writer, src io.Reader) error {
	cmd := exec.CommandContext(ctx, c.binary, c.compressArgs()...)
	cmd.Stdin = src
	cmd.Stdout = dst
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xz failed: %w (%s)", err, stderr.String())
	}
	return nil
}

// Extension returns the file extension for xz streams.
func (c *XZCLICompressor) Extension() string { return "xz" }

// ContentType returns the MIME type for xz streams.
func (c *XZCLICompressor) ContentType() string { return "application/x-xz" }

// Decompress runs `xz -dc -T0` under the memory bounds: multi-threaded decode
// on multi-block archives, stdin to stdout. Satisfies compression.Decompressor
// so xz uploads can be transcoded to the deploy format.
func (c *XZCLICompressor) Decompress(ctx context.Context, dst io.Writer, src io.Reader) error {
	cmd := exec.CommandContext(ctx, c.binary, c.decompressArgs()...)
	cmd.Stdin = src
	cmd.Stdout = dst
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xz -d failed: %w (%s)", err, stderr.String())
	}
	return nil
}

var _ compression.Decompressor = (*XZCLICompressor)(nil)
