//go:build (unix && (aix || solaris)) || (!unix && !windows)

package ytdlp

import (
	"errors"
	"os"
)

// errLockUnsupported is tryLock on a platform without flock.
var errLockUnsupported = errors.New("file locks are not supported on this platform")

// tryLock cannot lock here, and says so. Every caller treats that as
// inconclusive: nothing is ever promoted, and the cached copy is never
// replaced under a running yank.
func tryLock(f *os.File, exclusive bool) (granted bool, err error) {
	return false, errLockUnsupported
}
