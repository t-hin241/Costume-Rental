package media_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"webee_rentor/internal/media"
)

func picture() image.Image { return image.NewRGBA(image.Rect(0, 0, 4, 3)) }

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, picture()); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, picture(), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRealImagesAreAccepted(t *testing.T) {
	cases := map[string]struct {
		data []byte
		want string
	}{
		"jpeg": {jpegBytes(t), "image/jpeg"},
		"png":  {pngBytes(t), "image/png"},
		"webp": {[]byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00"), "image/webp"},
	}
	for name, c := range cases {
		f, err := media.DetectImage(c.data)
		if err != nil || f.ContentType != c.want {
			t.Errorf("%s: format = %+v, %v", name, f, err)
		}
	}
}

func TestAnythingElseIsRefused(t *testing.T) {
	var gifFile bytes.Buffer
	if err := gif.Encode(&gifFile, picture(), nil); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"nothing":          nil,
		"text":             []byte("xin chào"),
		"html":             []byte("<!doctype html><script>alert(1)</script>"),
		"svg":              []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
		"pdf":              []byte("%PDF-1.7"),
		"gif":              gifFile.Bytes(),
		"riff audio":       []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00"),
		"png header only":  []byte("\x89PNG\r\n\x1a\n<script>alert(1)</script>"),
		"jpeg header only": append([]byte{0xFF, 0xD8, 0xFF}, []byte("<script>alert(1)</script>")...),
	}
	for name, data := range cases {
		if _, err := media.DetectImage(data); !errors.Is(err, media.ErrUnsupportedImage) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// A few bytes can declare a picture no browser should be asked to open.
func TestAHugePictureIsRefused(t *testing.T) {
	data := pngBytes(t)
	binary.BigEndian.PutUint32(data[16:], 30_000) // width
	binary.BigEndian.PutUint32(data[20:], 30_000) // height
	binary.BigEndian.PutUint32(data[29:], crc32.ChecksumIEEE(data[12:29]))
	if _, err := media.DetectImage(data); !errors.Is(err, media.ErrFileTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestStoredNamesAreOurs(t *testing.T) {
	const id = "0193b0f2-7a1c-4f3e-9a55-0123456789ab"
	f, err := media.DetectImage(pngBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := media.FormatOf(media.FileName(id, f)); !ok || got != f {
		t.Fatalf("format of a stored name = %+v, %v", got, ok)
	}
	for _, name := range []string{"", ".png", "x.png", id, id + ".svg", id + ".png.tmp", id + ".PNG", "../" + id + ".png", "a/" + id + ".png", `a\` + id + ".png", id + ".png/"} {
		if _, ok := media.FormatOf(name); ok {
			t.Errorf("%q passed as a stored name", name)
		}
	}
}
