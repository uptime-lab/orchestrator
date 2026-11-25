package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAgentVersionsNumericOrder(t *testing.T) {
	dir := t.TempDir()
	client, err := NewLocalStorageClient(dir, "")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	sub := filepath.Join(dir, "agent-artifacts")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	for _, v := range []string{"0.2.9", "0.2.14", "0.2.10", "latest", "0.1.0", "0.2.2"} {
		name := "orchestrator-agent_" + v + "_linux_arm64.deb"
		if err := os.WriteFile(filepath.Join(sub, name), []byte("x"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	versions, err := client.ListAgentVersions("agent-artifacts")
	if err != nil {
		t.Fatalf("ListAgentVersions failed: %v", err)
	}
	want := []string{"latest", "0.2.14", "0.2.10", "0.2.9", "0.2.2", "0.1.0"}
	if len(versions) != len(want) {
		t.Fatalf("got %v, want %v", versions, want)
	}
	for i := range want {
		if versions[i] != want[i] {
			t.Fatalf("order mismatch at %d: got %v, want %v", i, versions, want)
		}
	}
}

// failingReader returns some data and then fails, simulating an interrupted upload.
type failingReader struct {
	data io.Reader
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.done {
		n, err := r.data.Read(p)
		if err == io.EOF {
			r.done = true
			return n, nil
		}
		return n, err
	}
	return 0, errors.New("connection reset")
}

func TestStoreWithOptionsAtomicOverwrite(t *testing.T) {
	client, err := NewLocalStorageClient(t.TempDir(), "")
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	ctx := context.Background()

	// Store the initial file.
	if _, err := client.StoreWithOptions(ctx, "acc/img.img", strings.NewReader("original"), nil); err != nil {
		t.Fatalf("initial store failed: %v", err)
	}

	// A failing overwrite must leave the original file untouched.
	_, err = client.StoreWithOptions(ctx, "acc/img.img", &failingReader{data: strings.NewReader("partial")}, nil)
	if err == nil {
		t.Fatal("expected store to fail")
	}

	reader, err := client.Retrieve(ctx, "acc/img.img")
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	defer reader.Close()
	content, _ := io.ReadAll(reader)
	if string(content) != "original" {
		t.Errorf("original file was corrupted by a failed overwrite: %q", content)
	}

	// No temp file should be left behind.
	entries, err := os.ReadDir(filepath.Join(client.baseDir, "acc"))
	if err != nil {
		t.Fatalf("failed to list dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	// A successful overwrite replaces the content.
	if _, err := client.StoreWithOptions(ctx, "acc/img.img", strings.NewReader("replacement"), nil); err != nil {
		t.Fatalf("overwrite failed: %v", err)
	}
	reader2, err := client.Retrieve(ctx, "acc/img.img")
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	defer reader2.Close()
	content2, _ := io.ReadAll(reader2)
	if string(content2) != "replacement" {
		t.Errorf("overwrite did not replace content: %q", content2)
	}
}
