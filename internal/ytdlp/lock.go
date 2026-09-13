package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// The bin-dir lock (#26). A yt-dlp zipapp cannot be replaced under a process
// running it: CPython's zipimport reads the archive's directory once but
// reopens the file by path for every module it loads later, so a probe or a
// download still running from the cached path fails with a zipimport error
// the moment a rename puts a different archive there. The PyInstaller bundle
// on unix re-execs itself by path, which has the same shape.
//
// So nothing removes or renames over the cached path while any yank might be
// running it. Every yank that uses the cached copy holds a shared lock on a
// file beside it, taken in Resolve before anything executes the cached path
// and kept until the process exits. Only a caller that holds the lock
// exclusively, which is proof that no other yank is running, may change what
// is at the cached path, and there are two such callers: promote, which puts
// a staged update in place (stage.go), and Resolve's repair of a cached copy
// that is positively unusable, which discards it and installs a fresh one.
// An update never touches the cached path itself; it stages beside it.
//
// The one replacement made without the exclusive lock is a first run, when
// the stat Resolve took under its shared lock found nothing cached. Two first
// runs can race, and the later rename lands on the file the earlier one may be
// probing; both fetched the same checksum-verified asset, so the file a
// reopen by path finds is byte-identical and the zipimport directory still
// matches it. The residual is two first launches that choose different assets
// (one PATH with a usable python3, one without) or straddle a release. A first
// run never overlaps a repair: the repair holds the lock exclusively from its
// discard to its install, so no yank can stat the path meanwhile, and a yank
// that already holds the shared lock stops the repair from starting.
//
// The lock protects yank processes, not their children. The lock file's
// descriptor is close-on-exec, so a yt-dlp child that outlives its yank — the
// yank crashed or was SIGKILLed mid-download — holds no lock, and a later yank
// may promote or repair under it. That is accepted: it needs a crash, and
// what it costs is that one orphaned download.

// ErrInUse reports that the exclusive lock was refused because another
// process holds the lock: another yank is running, and may be running the
// cached copy.
var ErrInUse = errors.New("another yank is using the cached yt-dlp")

// errUnlocked reports that this process cannot hold the lock at all, so it
// cannot know whether another yank is running the cached copy.
var errUnlocked = errors.New("the lock beside the cached yt-dlp cannot be held")

// lockPoll is how often a wait for the shared lock looks again. The exclusive
// lock is held for a promotion — a stat, a read, a rename and a write — or for
// a repair, which downloads and probes a fresh copy and so can keep a waiter
// for as long as a first-run download takes. Either way the wait honours the
// waiter's context.
const lockPoll = 10 * time.Millisecond

// lockPath is the lock file for binary.
func lockPath(binary string) string {
	return binary + ".lock"
}

// binLocks is the locks this process holds, one open lock file per binary.
// A lock file stays open, holding the shared lock, until the process exits:
// the lock is released by the kernel when the last descriptor closes, which
// is also what releases it for a yank that crashed.
type binLocks struct {
	mu   sync.Mutex
	held map[string]*os.File
	// unlocked is the lock files this process could not hold the shared lock
	// on, with the reason. It is sticky: a process that ran unlocked, even
	// once, may have run the cached copy with nothing to show for it, and a
	// lock that can be had later proves nothing about that, or about the
	// yanks that started meanwhile. Such a process never takes the exclusive
	// lock for that binary again.
	unlocked map[string]error
}

// processLocks is the process's own binLocks, the one Resolve and Update use.
var processLocks binLocks

// withExclusive runs fn while holding the exclusive lock for binary, provided
// it can be had without waiting, and in every case returns with this process
// holding the shared lock, if a lock can be held at all. refused is nil when
// fn ran, and says why it did not otherwise.
//
// The outcomes, and what each means:
//
//   - The exclusive lock was granted: no other yank holds the lock, so none is
//     running the cached copy, and fn may remove or rename over it.
//   - It was refused because another process holds the lock: fn does not run,
//     and refused satisfies errors.Is(ErrInUse). That is the ordinary case of
//     two yank windows, not a failure.
//   - The lock could not be taken or checked for any other reason, or this
//     process has already run unlocked: fn does not run, since nothing proves
//     that no other yank is running, and refused satisfies
//     errors.Is(errUnlocked). Inconclusive.
//
// The lock file is opened for writing, creating it, and failing that for
// reading: flock needs no write access, and a lock file left unwritable (by a
// yank run with sudo, say) must not leave this process running unlocked while
// another yank could still take the lock exclusively. fn runs with l's mutex
// held, so it must not call back into l.
//
// Afterwards the shared lock is taken, waiting while another process holds
// the exclusive one. A lock that cannot be held at all leaves this process
// unlocked, for good; err is set only when ctx ended the wait, and then the
// process holds no lock for binary.
//
// A process that already holds the shared lock converts its own lock. flock
// does not convert atomically — a refused conversion may have released the
// shared lock — which is why the shared lock is always taken again after.
func (l *binLocks) withExclusive(ctx context.Context, binary string, fn func()) (refused, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	path := lockPath(binary)
	if cause, ok := l.unlocked[path]; ok {
		return cause, nil
	}
	f := l.held[path]
	if f == nil {
		f, err = openLock(path)
		if err != nil {
			return l.markUnlocked(path, err), nil
		}
	}
	switch granted, lerr := tryLock(f, true); {
	case lerr != nil:
		refused = fmt.Errorf("%w: %w", errUnlocked, lerr)
	case !granted:
		refused = ErrInUse
	default:
		fn()
	}

	for {
		granted, lerr := tryLock(f, false)
		if lerr != nil {
			l.drop(path, f)
			l.markUnlocked(path, lerr)
			return refused, nil
		}
		if granted {
			if l.held == nil {
				l.held = map[string]*os.File{}
			}
			l.held[path] = f
			return refused, nil
		}
		select {
		case <-ctx.Done():
			l.drop(path, f)
			return refused, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// openLock opens the lock file at path for writing, creating it, or, when
// that fails, for reading. The error is the first open's, which is the one
// that says why the file could not be created or written.
func openLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err == nil {
		return f, nil
	}
	if f, rerr := os.Open(path); rerr == nil {
		return f, nil
	}
	return nil, err
}

// markUnlocked records that this process could not hold the lock at path,
// and returns the reason as every later refusal reports it.
func (l *binLocks) markUnlocked(path string, cause error) error {
	if l.unlocked == nil {
		l.unlocked = map[string]error{}
	}
	err := fmt.Errorf("%w: %w", errUnlocked, cause)
	l.unlocked[path] = err
	return err
}

// drop closes f, which releases whatever lock it held, and forgets it.
func (l *binLocks) drop(path string, f *os.File) {
	f.Close()
	delete(l.held, path)
}

// releaseAll closes every lock file, releasing the locks, and forgets which
// lock files could not be held. The process never calls it; a test does, so
// one test's locks cannot outlive it.
func (l *binLocks) releaseAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for path, f := range l.held {
		l.drop(path, f)
	}
	l.unlocked = nil
}
