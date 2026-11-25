package compression

import (
	"context"
	"io"
)

// Compressor compresses a stream into a device-deployable format.
type Compressor interface {
	// Compress reads src and writes the compressed stream to dst.
	Compress(ctx context.Context, dst io.Writer, src io.Reader) error
	// Extension returns the file extension without a dot, e.g. "zst".
	Extension() string
	// ContentType returns the MIME type of the compressed stream.
	ContentType() string
}

// Decompressor expands a compressed stream back to raw bytes. Used to
// transcode already-compressed uploads (e.g. .xz) into the deploy format.
type Decompressor interface {
	// Decompress reads the compressed src and writes raw bytes to dst.
	Decompress(ctx context.Context, dst io.Writer, src io.Reader) error
	// Extension returns the source extension this decompressor handles, e.g. "xz".
	Extension() string
}
