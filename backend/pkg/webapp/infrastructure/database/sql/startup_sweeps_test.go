package sql

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// A tracked file version is capped at 512 KiB, so the batch size that is right
// for a table of small JSON payloads is an OOM on a table of file contents: 500
// rows is ~256 MB held at once, against a container limit of a few hundred MB.
// This pins the two batch sizes apart, with the arithmetic that makes one of
// them wrong.
func TestTheLargeRowSweepBatchFitsInTheContainer(t *testing.T) {
	const containerBytes = 256 << 20 // a deliberately small container memory budget

	maxVersionBytes := node_file.MaxContentBytes
	if maxVersionBytes <= 0 {
		t.Fatalf("the per-version cap reads as %d; this test's arithmetic depends on it", maxVersionBytes)
	}

	if worst := contentBackfillBatchSize * maxVersionBytes; worst > containerBytes/8 {
		t.Fatalf("a full batch is %d bytes, more than an eighth of the %d-byte container limit",
			worst, containerBytes)
	}
	// And the general batch size would NOT fit — which is the reason the two
	// exist separately. If this ever stops holding, the split has become
	// pointless and should be removed rather than left as decoration.
	if worst := backfillBatchSize * maxVersionBytes; worst <= containerBytes/8 {
		t.Fatalf("the general batch size (%d rows) now fits large rows too (%d bytes); the split is no longer needed",
			backfillBatchSize, worst)
	}
}

// The repository sweeps are not the only thing that pages over node_files: the
// key-rotation rewrap and the boot-time coverage canary read the same column,
// and they read it through the declaration below. A column of large values that
// declares the small-row batch there is the same OOM in a place nobody looks at
// until a rotation or a restart.
func TestEveryEncryptedColumnDeclaresASweepBatchThatFitsItsValues(t *testing.T) {
	const containerBytes = 256 << 20 // a deliberately small container memory budget

	for _, c := range append(encryptedStringColumns(), nodeFilesContentColumn()) {
		if c.batch <= 0 {
			t.Errorf("%s.%s declares no sweep batch; a page of it is unbounded", c.table, c.col)
		}
	}

	content := nodeFilesContentColumn()
	if content.batch != contentBackfillBatchSize {
		t.Fatalf("node_files.content sweeps %d rows at a time; it holds %d-byte file versions and must use the large-row batch (%d)",
			content.batch, node_file.MaxContentBytes, contentBackfillBatchSize)
	}
	if worst := content.batch * node_file.MaxContentBytes; worst > containerBytes/8 {
		t.Fatalf("a node_files.content page is %d bytes, more than an eighth of the %d-byte container limit",
			worst, containerBytes)
	}
}
