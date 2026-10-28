//go:build !windows

package herdrmux

import (
	"os/exec"
	"syscall"
)

// detach makes cmd outlive the caller (new session, no controlling tty).
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
