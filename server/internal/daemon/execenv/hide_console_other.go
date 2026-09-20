//go:build !windows

package execenv

import "os/exec"

func hideConsoleLeaf(_ *exec.Cmd) {}
