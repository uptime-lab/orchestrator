package compression

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

// Every xz invocation is memory-bounded and told not to turn an automatic
// adjustment into exit status 2.
func TestXZArgsCarryTheMemoryBounds(t *testing.T) {
	c := &XZCLICompressor{binary: "xz", memLimitMB: 256}
	comp := strings.Join(c.compressArgs(), " ")
	for _, want := range []string{"-T0", "--no-warn", "--memlimit-compress=256MiB"} {
		if !strings.Contains(comp, want) {
			t.Errorf("compress args %q lack %q", comp, want)
		}
	}
	dec := strings.Join(c.decompressArgs(), " ")
	for _, want := range []string{"-dc", "-T0", "--no-warn", "--memlimit-mt-decompress=256MiB", "--memlimit-decompress=256MiB"} {
		if !strings.Contains(dec, want) {
			t.Errorf("decompress args %q lack %q", dec, want)
		}
	}
	if c2, err := NewXZCLICompressorWithMemLimit(0); err == nil && c2.memLimitMB <= 0 {
		t.Errorf("a zero limit must fall back to the default, not to unlimited")
	}
}

// A bound BELOW what -6 wants single-threaded (about 94 MiB) forces xz to
// adjust; without --no-warn that adjustment is exit status 2 and every
// compression on a constrained container fails.
func TestXZCompressSurvivesAnAdjustmentUnderATightBound(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz binary not installed")
	}
	c, err := NewXZCLICompressorWithMemLimit(32)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	payload := strings.Repeat("orchestrator os image payload ", 20000)
	var compressed bytes.Buffer
	if err := c.Compress(context.Background(), &compressed, strings.NewReader(payload)); err != nil {
		t.Fatalf("compression under a tight bound failed: %v", err)
	}
	var out bytes.Buffer
	if err := c.Decompress(context.Background(), &out, &compressed); err != nil {
		t.Fatalf("decompression under a tight bound failed: %v", err)
	}
	if out.String() != payload {
		t.Error("roundtrip payload mismatch")
	}
}

// The hard decompression bound refuses an archive whose dictionary alone
// exceeds it, with an error rather than an allocation.
func TestXZDecompressRefusesAnArchiveBeyondTheHardBound(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz binary not installed")
	}
	// -9 declares a 64 MiB dictionary; decoding needs about that much.
	big := exec.Command("xz", "-9", "-c", "-T1")
	big.Stdin = strings.NewReader(strings.Repeat("payload ", 4096))
	archive, err := big.Output()
	if err != nil {
		t.Fatalf("building the -9 archive: %v", err)
	}
	c, err := NewXZCLICompressorWithMemLimit(16)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	var out bytes.Buffer
	if err := c.Decompress(context.Background(), &out, bytes.NewReader(archive)); err == nil {
		t.Fatal("a 64 MiB-dictionary archive decoded under a 16 MiB hard bound")
	}
}

func TestXZCLICompressorRoundtrip(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz binary not installed")
	}

	compressor, err := NewXZCLICompressor()
	if err != nil {
		t.Fatalf("failed to create compressor: %v", err)
	}

	payload := strings.Repeat("orchestrator os image payload ", 100000)
	var compressed bytes.Buffer
	if err := compressor.Compress(context.Background(), &compressed, strings.NewReader(payload)); err != nil {
		t.Fatalf("compression failed: %v", err)
	}

	if compressed.Len() == 0 || compressed.Len() >= len(payload) {
		t.Fatalf("unexpected compressed size %d for %d raw bytes", compressed.Len(), len(payload))
	}

	// Verify with the same binary: xz -d -c must reproduce the input.
	cmd := exec.Command("xz", "-d", "-c")
	cmd.Stdin = &compressed
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("decompression failed: %v", err)
	}
	if string(out) != payload {
		t.Error("roundtrip payload mismatch")
	}
}

// The deploy format is MULTI-BLOCK xz (docs/OS-IMAGES.md): the device decodes
// one block per core. Under a memory bound xz drops threads first, and below
// ~166 MiB (measured, xz 5.8.3) it falls back to the single-threaded encoder,
// which emits ONE block. The default bound must stay above that cliff, and
// config.MinImageXZMemLimitMB is what keeps an operator from tuning below it.
func TestXZDefaultBoundKeepsTheMultiBlockFormat(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz binary not installed")
	}
	if runtime.NumCPU() < 2 {
		t.Skip("-T0 resolves to one thread on a single-core host and xz then picks the single-threaded encoder regardless of the bound")
	}
	for _, mb := range []int{config.DefaultImageXZMemLimitMB, config.MinImageXZMemLimitMB} {
		c, err := NewXZCLICompressorWithMemLimit(mb)
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		// Bigger than one -6 block (3 x 8 MiB), so a multi-block stream has
		// something to split.
		payload := bytes.Repeat([]byte("orchestrator os image payload "), (30<<20)/30)
		var compressed bytes.Buffer
		if err := c.Compress(context.Background(), &compressed, bytes.NewReader(payload)); err != nil {
			t.Fatalf("compress at %d MiB: %v", mb, err)
		}
		if blocks := xzBlockCount(t, compressed.Bytes()); blocks < 2 {
			t.Fatalf("at %d MiB the archive has %d block(s); the deploy format needs more than one", mb, blocks)
		}
	}
}

// xzBlockCount asks the real binary: `xz --robot --list` prints one `file`
// line whose third column is the block count.
func xzBlockCount(t *testing.T, archive []byte) int {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "blocks-*.xz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(archive); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command("xz", "--robot", "--list", f.Name()).Output()
	if err != nil {
		t.Fatalf("xz --list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) > 2 && fields[0] == "file" {
			n, err := strconv.Atoi(fields[2])
			if err != nil {
				t.Fatalf("block count %q: %v", fields[2], err)
			}
			return n
		}
	}
	t.Fatal("no file line in xz --list output")
	return 0
}
