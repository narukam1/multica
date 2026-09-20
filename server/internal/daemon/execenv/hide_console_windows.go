//go:build windows

package execenv

import (
	"os/exec"
	"syscall"
)

// createNoWindow (CREATE_NO_WINDOW) hides a leaf console process. Use this
// for git / cmd / mklink — they do not spawn console grandchildren that
// need an inherited console. Do not use it on cursor-agent; that is
// hideAgentWindow's CREATE_NEW_CONSOLE path (#1521).
const createNoWindow = 0x08000000

func hideConsoleLeaf(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
