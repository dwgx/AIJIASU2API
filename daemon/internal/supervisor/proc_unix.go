//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child into its own process group (Setpgid) so the
// whole tree can be signalled later with killProcessGroup. This file is the
// only place the supervisor touches SysProcAttr or raw signals.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the child's whole process group. The child was
// started with Setpgid, so its pid is also the group id; killing -pid reaches
// grandchildren that ignored the parent's exit. A process that is already gone
// reports ESRCH, which is the desired end state, so the error is dropped.
func killProcessGroup(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
