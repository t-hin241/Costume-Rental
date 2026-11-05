package mediafs_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"webee_rentor/internal/media"
	"webee_rentor/internal/media/mediafs"
)

func open(t *testing.T, dir string) *mediafs.Store {
	t.Helper()
	s, err := mediafs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAFileGoesInAndComesOutWhole(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir)
	if err := s.Put(ctx, "a.png", []byte("ảnh")); err != nil {
		t.Fatal(err)
	}
	f, err := s.Open(ctx, "a.png")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	if string(got) != "ảnh" || f.ModTime.IsZero() {
		t.Fatalf("read %q, modified %s", got, f.ModTime)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.png" {
		t.Fatalf("folder = %v, %v; want only the file, no temporary one", entries, err)
	}

	for range 2 { // deleting twice is fine
		if err := s.Delete(ctx, "a.png"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Open(ctx, "a.png"); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("after delete: err = %v", err)
	}
}

func TestReadsCannotLeaveTheFolder(t *testing.T) {
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "secret.png"), []byte("bí mật"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, filepath.Join(parent, "media"))
	if f, err := s.Open(context.Background(), "../secret.png"); err == nil {
		_ = f.Close()
		t.Fatal("read a file outside the folder")
	}
}

// A bad mount must stop the server when it starts.
func TestAFolderThatCannotBeUsedIsReportedAtOnce(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mediafs.New(filepath.Join(file, "media")); err == nil {
		t.Fatal("a path under a file was accepted")
	}
}
