package config

import (
	"testing"
	"time"
)

// Below the floor xz emits single-block archives, so the effective bound
// clamps UP and says so; 0 stays "the default".
func TestXZMemLimitFloor(t *testing.T) {
	cases := []struct {
		configured int
		want       int
		clamped    bool
	}{
		{0, DefaultImageXZMemLimitMB, false},
		{128, MinImageXZMemLimitMB, true},
		{MinImageXZMemLimitMB, MinImageXZMemLimitMB, false},
		{1024, 1024, false},
	}
	for _, c := range cases {
		cfg := ImageConfig{XZMemLimitMB: c.configured}
		if got := cfg.EffectiveXZMemLimitMB(); got != c.want {
			t.Errorf("configured %d: effective %d, want %d", c.configured, got, c.want)
		}
		if got := cfg.XZMemLimitClamped(); got != c.clamped {
			t.Errorf("configured %d: clamped %v, want %v", c.configured, got, c.clamped)
		}
	}
	if DefaultImageXZMemLimitMB < MinImageXZMemLimitMB {
		t.Fatal("the default must sit above the multi-block floor")
	}
}

func TestCompressionAndStagingBoundsNeverUnlimited(t *testing.T) {
	var zero ImageConfig
	if zero.EffectiveMaxConcurrentCompressions() != DefaultImageMaxConcurrentCompressions {
		t.Fatal("0 compressions must mean the default")
	}
	if zero.EffectiveMaxStagingBytes() != DefaultImageMaxStagingBytes {
		t.Fatal("0 staging bytes must mean the default")
	}
}

// The container-wide compression budget is derived from the
// per-process limit when unset, and can never drop below two process limits —
// a transcode is a decoder and an encoder and must be able to run at all.
func TestCompressionMemoryBudgetFloorsAtOneTranscode(t *testing.T) {
	var zero ImageConfig
	if got, want := zero.EffectiveCompressionMemoryBudgetMB(), 2*zero.EffectiveXZMemLimitMB(); got != want {
		t.Fatalf("unset budget = %d, want two process limits (%d)", got, want)
	}
	low := ImageConfig{XZMemLimitMB: 256, CompressionMemoryBudgetMB: 300}
	if got := low.EffectiveCompressionMemoryBudgetMB(); got != 512 {
		t.Fatalf("a budget below one transcode = %d, want 512", got)
	}
	if !low.CompressionMemoryBudgetClamped() {
		t.Fatal("the clamp must be reported so the boot log can say so")
	}
	high := ImageConfig{XZMemLimitMB: 256, CompressionMemoryBudgetMB: 2048}
	if got := high.EffectiveCompressionMemoryBudgetMB(); got != 2048 || high.CompressionMemoryBudgetClamped() {
		t.Fatalf("an explicit budget above the floor must stand: %d clamped=%v", got, high.CompressionMemoryBudgetClamped())
	}
}

func TestCgroupMemoryLimitIsZeroWhenUnknown(t *testing.T) {
	// On a developer machine there is no cgroup file; the check must read as
	// "unknown", never as a limit of zero bytes.
	if got := CgroupMemoryLimitMB(); got < 0 {
		t.Fatalf("cgroup limit = %d, want 0 or a positive MiB count", got)
	}
}

// With neither an explicit cap nor an upload cap, the expansion of a
// compressed source is still bounded, and so is a job's run time.
func TestDecompressionCapAndJobDeadlineHaveDefaults(t *testing.T) {
	var zero ImageConfig
	if got := zero.DecompressionLimit(); got != DefaultImageMaxDecompressedSize {
		t.Errorf("DecompressionLimit() = %d with nothing configured, want %d", got, DefaultImageMaxDecompressedSize)
	}
	if got := (ImageConfig{MaxUploadSize: 1 << 30}).DecompressionLimit(); got != 20<<30 {
		t.Errorf("DecompressionLimit() = %d with a 1 GiB upload cap, want 20 GiB", got)
	}
	if got := (ImageConfig{MaxDecompressedSize: 5}).DecompressionLimit(); got != 5 {
		t.Errorf("an explicit cap must win, got %d", got)
	}
	if got := zero.CompressionTimeout(); got != DefaultImageCompressionTimeoutMinutes*time.Minute {
		t.Errorf("CompressionTimeout() = %v with nothing configured, want the default", got)
	}
	if got := (ImageConfig{CompressionTimeoutMinutes: 7}).CompressionTimeout(); got != 7*time.Minute {
		t.Errorf("CompressionTimeout() = %v, want 7m", got)
	}
}
