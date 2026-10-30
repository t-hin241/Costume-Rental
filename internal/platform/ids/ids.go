// Package ids generates opaque random identifiers. IDs are not secrets and
// never grant access on their own.
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// New returns a random 128-bit identifier in UUID v4 text form.
func New() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails on supported platforms
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// codeAlphabet avoids look-alike characters (0/O, 1/I/L) for codes read
// aloud or typed by staff.
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// Code returns prefix + "-" + n random characters, e.g. BK-7KQ2M9XA, for
// human references such as booking codes.
func Code(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteByte('-')
	for _, v := range b {
		sb.WriteByte(codeAlphabet[int(v)%len(codeAlphabet)])
	}
	return sb.String()
}
