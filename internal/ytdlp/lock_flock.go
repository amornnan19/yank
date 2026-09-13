//go:build unix && !aix && !solaris

package ytdlp

import (
	"errors"
	"os"
	"syscall"
)

// tryLock asks for f's lock without waiting: exclusive, or shared. granted is
// false with a nil error only when another open file holds a lock that
// conflicts, which is the one answer that says something about other yanks.
// Any other failure — EINTR, ENOLCK on a filesystem without locks, EBADF — is
// returned as err and means the question was not answered.
//
// flock locks belong to the open file, not to the process, so a second open
// of the same lock file in this process conflicts exactly as another process
// would. The tests rely on that.
func tryLock(f *os.File, exclusive bool) (granted bool, err error) {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}
