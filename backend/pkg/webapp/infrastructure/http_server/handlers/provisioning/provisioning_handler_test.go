package provisioning

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeTempFile creates dir/rel (with parents) containing content.
func writeTempFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestCollectPayloadFiles_ReturnsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, dir, "config.txt", "arm_64bit=1")
	writeTempFile(t, dir, "cmdline.txt", "console=tty1")
	writeTempFile(t, dir, filepath.Join("overlays", "disable-bt.dtbo"), "dtbo")

	files, err := CollectPayloadFiles(dir)
	if err != nil {
		t.Fatalf("CollectPayloadFiles: %v", err)
	}
	sort.Strings(files)

	want := []string{"cmdline.txt", "config.txt", filepath.Join("overlays", "disable-bt.dtbo")}
	if len(files) != len(want) {
		t.Fatalf("expected %d files, got %d: %v", len(want), len(files), files)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("file[%d]: want %q, got %q", i, want[i], files[i])
		}
	}
}

func TestCollectPayloadFiles_EmptyDirYieldsNoFiles(t *testing.T) {
	// An empty payload dir must yield zero files; the handler treats this as
	// "SD provisioning payload is empty" and refuses to create a token.
	dir := t.TempDir()
	files, err := CollectPayloadFiles(dir)
	if err != nil {
		t.Fatalf("CollectPayloadFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no files for empty dir, got %v", files)
	}
}

func TestCollectPayloadFiles_NonexistentDirErrors(t *testing.T) {
	_, err := CollectPayloadFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent payload dir, got nil")
	}
}

func TestBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]string{
		"config.txt":                            "arm_64bit=1",
		filepath.Join("overlays", "a.dtbo"):     "overlay-a",
		filepath.Join("overlays", "sub", "b.x"): "nested",
	}
	for rel, content := range payload {
		writeTempFile(t, dir, rel, content)
	}

	files, err := CollectPayloadFiles(dir)
	if err != nil {
		t.Fatalf("CollectPayloadFiles: %v", err)
	}

	// Assemble a zip exactly like the handler does, then add the adoption file.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		if err := writeFileToZip(zw, dir, f); err != nil {
			t.Fatalf("writeFileToZip %s: %v", f, err)
		}
	}
	const adoption = "eyJtYXN0ZXJfdG9rZW4iOiJtdF90ZXN0In0="
	w, err := zw.Create(AdoptionFileName)
	if err != nil {
		t.Fatalf("create adoption entry: %v", err)
	}
	if _, err := w.Write([]byte(adoption)); err != nil {
		t.Fatalf("write adoption entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	// Read the zip back and verify every entry.
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}

	got := map[string]string{}
	for _, e := range zr.File {
		rc, err := e.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", e.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry %s: %v", e.Name, err)
		}
		got[e.Name] = string(data)
	}

	// The adoption file must be present at the root with the exact content.
	if got[AdoptionFileName] != adoption {
		t.Errorf("adoption file: want %q, got %q", adoption, got[AdoptionFileName])
	}

	// Every payload file must be present under a forward-slash path with matching content.
	for rel, content := range payload {
		key := filepath.ToSlash(rel)
		if got[key] != content {
			t.Errorf("payload %q: want %q, got %q", key, content, got[key])
		}
	}

	// Zip entry names must never contain backslashes, even on Windows-style inputs.
	for name := range got {
		if bytes.ContainsRune([]byte(name), '\\') {
			t.Errorf("zip entry %q contains a backslash", name)
		}
	}
}
