package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkSharedDirRoundTripKeepsTarget(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "marker.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	destRoot := t.TempDir()
	dest := filepath.Join(destRoot, "standard")

	if err := linkSharedDir(src, dest, destRoot); err != nil {
		t.Fatalf("linkSharedDir: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "marker.txt"))
	if err != nil {
		t.Fatalf("read through shared dir: %v", err)
	}
	if string(body) != "keep\n" && string(body) != "keep\r\n" {
		t.Fatalf("marker = %q", body)
	}
	if same, err := sharedDirAlreadyLinked(dest, src); err != nil || !same {
		t.Fatalf("already-linked = %v, %v", same, err)
	}
	if err := linkSharedDir(src, dest, destRoot); err != nil {
		t.Fatalf("idempotent linkSharedDir: %v", err)
	}

	if err := unlinkSharedDir(dest); err != nil {
		t.Fatalf("unlinkSharedDir: %v", err)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("dest should be gone, lstat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "marker.txt")); err != nil {
		t.Fatalf("unlinking walked into the authoritative dir: %v", err)
	}
}

func TestUnlinkSharedDirRefusesRealDirectory(t *testing.T) {
	dir := t.TempDir()
	err := unlinkSharedDir(dir)
	if err == nil {
		t.Fatal("expected refuse")
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("real directory was removed: %v", statErr)
	}
}

func TestReplaceDestForSharedLinkAllowsLeftoverCopyInsideDestRoot(t *testing.T) {
	destRoot := t.TempDir()
	dest := filepath.Join(destRoot, "standard")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "stale.txt"), []byte("copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "marker.txt"), []byte("auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := linkSharedDir(src, dest, destRoot); err != nil {
		t.Fatalf("replace leftover copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("stale copy still visible through the new link")
	}
	body, err := os.ReadFile(filepath.Join(dest, "marker.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "auth\n" && string(body) != "auth\r\n" {
		t.Fatalf("marker = %q", body)
	}
}
