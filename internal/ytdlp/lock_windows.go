//go:build windows

package ytdlp

import "os"

// tryLock grants every lock on Windows without taking one.
//
// The hazard the lock guards against does not arise there. Windows only ever
// installs the PyInstaller bundle, never the zipapp (see chooseAsset), and the
// bundle does not reopen itself by path while it runs; and Windows refuses to
// rename over an image a running process has mapped. So a promotion that finds
// another yank running yt-dlp fails at the rename, which promote classifies as
// inconclusive and keeps the staged copy for a later launch. Taking a real
// lock would need LockFileEx, which the standard library's syscall package
// does not export; calling it through a lazily loaded kernel32 would add
// platform code no test here can run, to guard against a hazard the OS
// already refuses.
func tryLock(f *os.File, exclusive bool) (granted bool, err error) {
	return true, nil
}
