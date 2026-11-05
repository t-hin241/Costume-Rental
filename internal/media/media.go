// Package media holds the rules for files the shop uploads: only real images,
// stored under names the server chooses. Where the bytes live is up to a Store.
package media

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg" // registers the decoder behind image.DecodeConfig
	_ "image/png"  // registers the decoder behind image.DecodeConfig
	"io"
	"path"
	"regexp"
	"time"

	"webee_rentor/internal/domainerr"
)

// maxPixels bounds what a browser must decode to show one picture.
const maxPixels = 50_000_000

var (
	ErrUnsupportedImage = domainerr.New(domainerr.KindInvalid, "media.unsupported_image", "only JPEG, PNG and WebP images are accepted")
	ErrFileTooLarge     = domainerr.New(domainerr.KindInvalid, "media.file_too_large", "the file is too large")
	ErrNotFound         = domainerr.New(domainerr.KindNotFound, "media.not_found", "no such file")
)

// Format is an accepted image format.
type Format struct {
	ContentType string
	Extension   string
}

var (
	jpegFormat = Format{ContentType: "image/jpeg", Extension: ".jpg"}
	pngFormat  = Format{ContentType: "image/png", Extension: ".png"}
	webpFormat = Format{ContentType: "image/webp", Extension: ".webp"}
)

// sniff identifies a format by its leading bytes. What the client calls the
// file is never consulted.
func sniff(data []byte) (Format, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return jpegFormat, true
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return pngFormat, true
	case len(data) >= 16 && string(data[:4]) == "RIFF" && string(data[8:15]) == "WEBPVP8":
		return webpFormat, true
	}
	return Format{}, false
}

// DetectImage returns the format of an accepted image. JPEG and PNG headers are
// parsed to refuse a file that merely starts like one, and oversized pictures;
// the standard library has no WebP decoder, so WebP is judged by its header.
func DetectImage(data []byte) (Format, error) {
	f, ok := sniff(data)
	if !ok {
		return Format{}, ErrUnsupportedImage
	}
	if f == webpFormat {
		return f, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Format{}, ErrUnsupportedImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return Format{}, ErrFileTooLarge
	}
	return f, nil
}

var fileName = regexp.MustCompile(`^[0-9a-f-]{36}\.(jpg|png|webp)$`)

// FileName is the stored name: a server-made id and the detected extension.
func FileName(id string, f Format) string { return id + f.Extension }

// FormatOf returns the format of a stored name. Anything else, including a path
// that tries to leave the folder, is not a file of ours.
func FormatOf(name string) (Format, bool) {
	if !fileName.MatchString(name) {
		return Format{}, false
	}
	switch path.Ext(name) {
	case jpegFormat.Extension:
		return jpegFormat, true
	case pngFormat.Extension:
		return pngFormat, true
	}
	return webpFormat, true
}

// File is a stored file open for reading.
type File struct {
	io.ReadSeekCloser
	ModTime time.Time
}

// Store keeps uploaded files. Names are already validated.
type Store interface {
	// Put writes the whole file or nothing.
	Put(ctx context.Context, name string, data []byte) error
	// Open reads a file; a missing one matches ErrNotFound.
	Open(ctx context.Context, name string) (File, error)
	Delete(ctx context.Context, name string) error
}
