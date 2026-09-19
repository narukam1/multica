//go:build !windows

package execenv

import "os"

func createSharedDirLink(src, dest string) error {
	return os.Symlink(src, dest)
}

func isDirLink(fi os.FileInfo) bool {
	return fi.Mode()&os.ModeSymlink != 0
}
