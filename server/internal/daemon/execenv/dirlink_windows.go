//go:build windows

package execenv

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// createSharedDirLink always uses a directory junction. os.Symlink on Windows
// needs Developer Mode or admin and produces a different object than
// worktree-ops-reference (mklink /J). Junctions do not require elevation and
// are what Java / Maven / CodeGraph already resolve on this team's machines.
func createSharedDirLink(src, dest string) error {
	cmd := exec.Command("cmd", "/c", "mklink", "/J", dest, src)
	hideConsoleLeaf(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J %s %s: %s: %w", dest, src, strings.TrimSpace(string(out)), err)
	}
	return nil
}

func isDirLink(fi os.FileInfo) bool {
	m := fi.Mode()
	if m&os.ModeSymlink != 0 {
		return true
	}
	// Go 1.23+: a directory junction is ModeDir|ModeIrregular, no ModeSymlink.
	return fi.IsDir() && m&os.ModeIrregular != 0
}
