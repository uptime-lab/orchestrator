package config

import (
	"strings"
	"testing"
)

// The xz bound is per PROCESS and a transcode is a decoder plus an encoder,
// so what a Pi-class host can survive is the budget the running jobs may
// hold, plus the Go process, inside the backend's container limit. The
// compose states every term of that sum explicitly — an unset budget is a
// default an operator raising the job count cannot see — and the sum must
// fit, or the boot log's own check would warn on a fresh install.
func TestCommunityComposeCompressionBudgetFitsTheContainer(t *testing.T) {
	compose := readCommunityCompose(t)

	img := ImageConfig{
		XZMemLimitMB:              composeDefaultInt(t, compose, "IMAGE_XZ_MEMLIMIT_MB"),
		CompressionMemoryBudgetMB: composeDefaultInt(t, compose, "IMAGE_COMPRESSION_MEMORY_BUDGET_MB"),
		MaxConcurrentCompressions: composeDefaultInt(t, compose, "IMAGE_MAX_CONCURRENT_COMPRESSIONS"),
	}
	limitMB := composeMemoryMB(t, compose, "BACKEND_MEMORY_LIMIT")

	if img.XZMemLimitClamped() {
		t.Errorf("IMAGE_XZ_MEMLIMIT_MB=%d sits below the multi-block floor %d", img.XZMemLimitMB, MinImageXZMemLimitMB)
	}
	if img.CompressionMemoryBudgetClamped() {
		t.Errorf("IMAGE_COMPRESSION_MEMORY_BUDGET_MB=%d is below two process limits (%d); a transcode could not run", img.CompressionMemoryBudgetMB, 2*img.EffectiveXZMemLimitMB())
	}
	if budget := img.EffectiveCompressionMemoryBudgetMB(); budget+CompressionGoHeadroomMB > limitMB {
		t.Errorf("compression budget %d MiB + %d MiB for the Go process exceeds the backend's %d MiB container limit", budget, CompressionGoHeadroomMB, limitMB)
	}
}

// The memory limit is read from the backend service's YAML, never by a text
// search for the first `${BACKEND_MEMORY_LIMIT:-…}`: a comment, another
// service or a second spelling earlier in the file would otherwise decide
// the number the budget is checked against.
func TestBackendMemoryLimitIsReadFromTheBackendService(t *testing.T) {
	compose := readCommunityCompose(t)
	const real = "    deploy:\n      resources:\n        limits:\n          memory: ${BACKEND_MEMORY_LIMIT:-1G}\n"
	if !strings.Contains(compose, real) {
		t.Fatalf("the community compose no longer states the backend limit as %q; update this test", real)
	}
	edit := func(replacement string) string { return strings.Replace(compose, real, replacement, 1) }

	got, err := backendMemoryLimitMB(compose, "BACKEND_MEMORY_LIMIT")
	if err != nil || got != 1024 {
		t.Fatalf("the shipped compose: got %d, %v; want 1024", got, err)
	}

	commented := edit("    # memory: ${BACKEND_MEMORY_LIMIT:-8G}\n" + real)
	if got, err := backendMemoryLimitMB(commented, "BACKEND_MEMORY_LIMIT"); err != nil || got != 1024 {
		t.Errorf("a comment above the limit: got %d, %v; want 1024 from the YAML", got, err)
	}

	elsewhere := strings.Replace(compose, "${POSTGRES_MEMORY_LIMIT:-1G}", "${BACKEND_MEMORY_LIMIT:-8G}", 1)
	if got, err := backendMemoryLimitMB(elsewhere, "BACKEND_MEMORY_LIMIT"); err != nil || got != 1024 {
		t.Errorf("the same variable on another service: got %d, %v; want the backend's 1024", got, err)
	}

	refused := map[string]string{
		"a second spelling (mem_limit)": edit("    mem_limit: ${BACKEND_MEMORY_LIMIT:-8G}\n" + real),
		"a duplicate memory key": edit("    deploy:\n      resources:\n        limits:\n" +
			"          memory: ${BACKEND_MEMORY_LIMIT:-8G}\n          memory: ${BACKEND_MEMORY_LIMIT:-1G}\n"),
		"no limit at all":  edit(""),
		"another variable": edit(strings.Replace(real, "BACKEND_MEMORY_LIMIT", "OTHER_LIMIT", 1)),
	}
	for name, text := range refused {
		if got, err := backendMemoryLimitMB(text, "BACKEND_MEMORY_LIMIT"); err == nil {
			t.Errorf("%s: read as %d MiB, want a refusal", name, got)
		}
	}
}
