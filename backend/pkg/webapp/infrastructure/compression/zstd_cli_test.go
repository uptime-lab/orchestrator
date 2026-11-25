package compression

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestZstdCLICompressorRoundtrip(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd binary not installed")
	}

	compressor, err := NewZstdCLICompressor()
	if err != nil {
		t.Fatalf("failed to create compressor: %v", err)
	}
	if compressor.Extension() != "zst" {
		t.Errorf("extension = %q, want zst", compressor.Extension())
	}

	payload := strings.Repeat("orchestrator os image payload ", 100000)
	var compressed bytes.Buffer
	if err := compressor.Compress(context.Background(), &compressed, strings.NewReader(payload)); err != nil {
		t.Fatalf("compression failed: %v", err)
	}

	if compressed.Len() == 0 || compressed.Len() >= len(payload) {
		t.Fatalf("unexpected compressed size %d for %d raw bytes", compressed.Len(), len(payload))
	}

	cmd := exec.Command("zstd", "-d", "-c")
	cmd.Stdin = &compressed
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("decompression failed: %v", err)
	}
	if string(out) != payload {
		t.Error("roundtrip payload mismatch")
	}
}
