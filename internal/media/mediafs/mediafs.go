// Package mediafs stores uploaded files in a folder on the server's disk. The
// folder must sit outside any web root and be part of the backups.
package mediafs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"webee_rentor/internal/media"
)

const (
	dirMode  = 0o750
	fileMode = 0o640
	// A partly written file has this suffix, which no stored name can have.
	partial = ".tmp"
	probe   = ".write-check" + partial
)

// Store is a media.Store in one folder. os.Root keeps every operation inside it.
type Store struct {
	root *os.Root
}

// New opens dir, creating it if needed, and proves it is writable so a bad
// mount stops the server at start rather than at the first upload.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("media folder: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("media folder: %w", err)
	}
	s := &Store{root: root}
	if err := s.write(probe, nil); err != nil {
		_ = root.Close() // the write error is the one to report
		return nil, fmt.Errorf("media folder is not writable: %w", err)
	}
	if err := root.Remove(probe); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("media folder: %w", err)
	}
	return s, nil
}

// Close releases the folder.
func (s *Store) Close() error { return s.root.Close() }

func (s *Store) write(name string, data []byte) error {
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// Put writes to a temporary name and renames, so readers never see half a file.
func (s *Store) Put(_ context.Context, name string, data []byte) error {
	tmp := name + partial
	if err := s.write(tmp, data); err != nil {
		return errors.Join(fmt.Errorf("write media file %s: %w", name, err), s.remove(tmp))
	}
	if err := s.root.Rename(tmp, name); err != nil {
		return errors.Join(fmt.Errorf("store media file %s: %w", name, err), s.remove(tmp))
	}
	return nil
}

func (s *Store) remove(name string) error {
	if err := s.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove media file %s: %w", name, err)
	}
	return nil
}

// Open reads a stored file.
func (s *Store) Open(_ context.Context, name string) (media.File, error) {
	f, err := s.root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return media.File{}, fmt.Errorf("media file %s: %w", name, media.ErrNotFound)
	}
	if err != nil {
		return media.File{}, fmt.Errorf("open media file %s: %w", name, err)
	}
	info, err := f.Stat()
	if err != nil {
		return media.File{}, errors.Join(fmt.Errorf("read media file %s: %w", name, err), f.Close())
	}
	return media.File{ReadSeekCloser: f, ModTime: info.ModTime()}, nil
}

// Delete removes a file; one already gone is not an error.
func (s *Store) Delete(_ context.Context, name string) error { return s.remove(name) }
