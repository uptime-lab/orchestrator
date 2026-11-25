package compression

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/compression"
)

// ZstdCLICompressor compresses streams by piping them through the system zstd
// binary. zstd is preferred over xz for OS images: the archive is ~10-15%
// larger, but devices decompress it 5-10x faster, which makes install_os
// network-bound instead of decoder-bound.
type ZstdCLICompressor struct {
	binary string
}

var _ compression.Compressor = (*ZstdCLICompressor)(nil)

// NewZstdCLICompressor returns a compressor backed by the system zstd binary,
// or an error if zstd is not installed.
func NewZstdCLICompressor() (*ZstdCLICompressor, error) {
	binary, err := exec.LookPath("zstd")
	if err != nil {
		return nil, fmt.Errorf("zstd binary not found in PATH: %w", err)
	}
	return &ZstdCLICompressor{binary: binary}, nil
}

// Compress runs `zstd -T2 -15 -c`: two threads (see below), high-but-practical level —
// level 19 compresses only ~5% smaller yet takes 3-4x longer on the ARM
// server, while devices decode any level equally fast.
func (c *ZstdCLICompressor) Compress(ctx context.Context, dst io.Writer, src io.Reader) error {
	// -T2, not -T0: the compression memory budget charges every codec the xz
	// process limit, and an all-core zstd at level 15 is not bounded by
	// anything that number describes. Two threads keep the
	// fallback's footprint in the same order as one bounded xz process; a
	// deploy that wants zstd at full speed should size the budget for it.
	cmd := exec.CommandContext(ctx, c.binary, "-T2", "-15", "-c")
	cmd.Stdin = src
	cmd.Stdout = dst
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("zstd failed: %w (%s)", err, stderr.String())
	}
	return nil
}

// Extension returns the file extension for zstd streams.
func (c *ZstdCLICompressor) Extension() string { return "zst" }

// ContentType returns the MIME type for zstd streams.
func (c *ZstdCLICompressor) ContentType() string { return "application/zstd" }

// Decompress runs `zstd -dc`, stdin to stdout. Satisfies
// compression.Decompressor so .zst uploads can be transcoded to the deploy
// format.
func (c *ZstdCLICompressor) Decompress(ctx context.Context, dst io.Writer, src io.Reader) error {
	cmd := exec.CommandContext(ctx, c.binary, "-dc")
	cmd.Stdin = src
	cmd.Stdout = dst
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("zstd -d failed: %w (%s)", err, stderr.String())
	}
	return nil
}

var _ compression.Decompressor = (*ZstdCLICompressor)(nil)
