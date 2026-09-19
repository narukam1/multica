package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// linkSharedDir mounts src at dest as a shared read-through directory.
//
// workspace-layout.yaml says isolation: junction for the *intent* (do not
// copy; do not worktree). The OS primitive is chosen here:
//
//   - Windows: directory junction (mklink /J). Matches srm-all
//     worktree-ops-reference; no Developer Mode; Java/Maven/CodeGraph treat
//     it as a real directory.
//   - Unix: directory symlink (ln -s / os.Symlink).
//
// destRoot bounds leftover real copies: a previous failed run may have left a
// real directory under the daemon workspace. Those may be removed. A path
// outside destRoot is never deleted. Existing links are only unlinked with
// os.Remove (rmdir), never RemoveAll — RemoveAll on a junction/symlink must
// not walk into the authoritative standard/ or .codegraph/.
func linkSharedDir(src, dest, destRoot string) error {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	destRootAbs, err := filepath.Abs(destRoot)
	if err != nil {
		return err
	}
	same, err := sharedDirAlreadyLinked(destAbs, srcAbs)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	if err := replaceDestForSharedLink(destAbs, destRootAbs); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destAbs), 0o755); err != nil {
		return err
	}
	return createSharedDirLink(srcAbs, destAbs)
}

// unlinkSharedDir removes dest only when it is a link. The target is left
// untouched — this is the Go equivalent of Windows `rmdir` on a junction.
func unlinkSharedDir(dest string) error {
	if dest == "" {
		return nil
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(destAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !isDirLink(fi) {
		return fmt.Errorf("workspace_layout: %s is not a link; refusing to unlink a real directory", destAbs)
	}
	return os.Remove(destAbs)
}

func sharedDirAlreadyLinked(dest, src string) (bool, error) {
	fi, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !isDirLink(fi) {
		return false, nil
	}
	target, err := os.Readlink(dest)
	if err != nil {
		return false, err
	}
	target = stripWindowsNTPrefix(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dest), target)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	return sameAbsPath(targetAbs, src), nil
}

func replaceDestForSharedLink(dest, destRoot string) error {
	fi, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if isDirLink(fi) {
		return os.Remove(dest)
	}
	if !pathUnderRoot(dest, destRoot) {
		return fmt.Errorf("workspace_layout: refusing to replace %s (outside dest root)", dest)
	}
	return os.RemoveAll(dest)
}

func pathUnderRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func sameAbsPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func stripWindowsNTPrefix(p string) string {
	for _, prefix := range []string{`\??\`, `\\?\`} {
		if strings.HasPrefix(p, prefix) {
			return p[len(prefix):]
		}
	}
	return p
}
