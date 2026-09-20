//go:build windows

package execenv

import (
	"os/exec"
	"testing"
)

func TestHideConsoleLeafSetsCreateNoWindow(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo", "hi")
	hideConsoleLeaf(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should be initialized")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("HideWindow should be true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CreationFlags should include CREATE_NO_WINDOW, got 0x%x", cmd.SysProcAttr.CreationFlags)
	}
	if cmd.SysProcAttr.CreationFlags&0x00000010 != 0 {
		t.Fatal("leaf processes must not use CREATE_NEW_CONSOLE")
	}
}

func TestGitCommandHidesConsole(t *testing.T) {
	cmd := gitCommand(".", "status")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatal("gitCommand should hide the console")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatal("gitCommand should set CREATE_NO_WINDOW")
	}
}
