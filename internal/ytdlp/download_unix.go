//go:build unix

package ytdlp

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup starts yt-dlp as the leader of a process group of its own.
//
// Setpgid makes the child's pgid equal to its own pid, which is what lets
// killProcessTree name the whole group with one negated pid. Without it the
// child joins yank's group and signalling that group would kill yank too.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree kills yt-dlp and every process it started.
//
// A negative pid names the process group, so this reaches the ffmpeg yt-dlp
// spawned to merge or transcode. Killing yt-dlp alone would orphan that ffmpeg
// and leave it burning a core on work the user has already abandoned.
//
// The caller has just checked that proc has not been reaped: signalling a group
// whose leader is gone can hit whatever the kernel has since recycled the pid
// for.
func killProcessTree(proc *os.Process) error {
	return syscall.Kill(-proc.Pid, syscall.SIGKILL)
}
