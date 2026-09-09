//go:build windows

package ytdlp

import (
	"os"
	"os/exec"
	"strconv"
)

// setProcessGroup does nothing on Windows.
//
// Windows has no process group of the unix shape to put a child in at spawn
// time, so there is nothing to arrange here; killProcessTree walks the child
// tree by pid instead.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessTree ends yt-dlp and every process it started.
//
// taskkill /T walks the child tree from pid, which is how the ffmpeg yt-dlp
// spawned to merge or transcode is reached; /F makes it unconditional.
//
// It takes no context deliberately: it is only ever reached from Cmd.Cancel,
// which runs because the caller's context is already done, and handing it that
// context would kill the killer before it did anything.
func killProcessTree(proc *os.Process) error {
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(proc.Pid)).Run()
}
