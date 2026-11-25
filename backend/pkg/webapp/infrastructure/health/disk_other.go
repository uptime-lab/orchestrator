//go:build !linux && !darwin

package health

import "errors"

// diskFree keeps the package buildable off the two platforms this ships on.
// Reporting an error (rather than "plenty of room") means such a build fails
// its readiness check loudly instead of claiming a filesystem it never read.
func diskFree(string) (uint64, uint64, uint64, error) {
	return 0, 0, 0, errors.New("filesystem statistics are not available on this platform")
}
