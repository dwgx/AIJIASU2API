//go:build windows

package supervisor

import (
	"os"
	"os/exec"
)

// setProcessGroup is a no-op: Windows has no POSIX process groups. Grandchild
// cleanup relies on the child's own behavior; Go's os.Process.Kill on Windows
// terminates exactly one process (TerminateProcess).
func setProcessGroup(_ *exec.Cmd) {}

// killProcessGroup falls back to a single-process kill. The supervisor passes
// the *os.Process whose pid this is; on Windows there is no group to signal.
func killProcessGroup(p *os.Process) {
	_ = p.Kill()
}
