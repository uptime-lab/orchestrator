// Package buildinfo carries what the running binary knows about its own
// build, set once at startup by the server's main package (which owns the
// link-time stamp) and read by the handlers that report it.
package buildinfo

import "sync/atomic"

var (
	revision atomic.Value
	version  atomic.Value
)

// SetRevision records the commit this binary was built from. An empty
// string means the build recorded none.
func SetRevision(r string) { revision.Store(r) }

// Revision reports the commit this binary was built from, or "" when the
// build recorded none. Nothing invents a value in its place.
func Revision() string {
	r, _ := revision.Load().(string)
	return r
}

// SetVersion records the platform release this binary was built for. An
// empty string means the build stamped none (a build from source).
func SetVersion(v string) { version.Store(v) }

// Version reports the platform release this binary was built for, or ""
// when the build stamped none.
func Version() string {
	v, _ := version.Load().(string)
	return v
}
