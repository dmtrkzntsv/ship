// Package sums computes, writes, parses and verifies SHA256SUMS files.
package sums

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Sums maps a file name to its lowercase hex SHA-256 digest.
type Sums map[string]string

// Compute hashes each named file in dir.
func Compute(dir string, files []string) (Sums, error) {
	s := make(Sums, len(files))
	for _, name := range files {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", name, err)
		}
		s[name] = hex.EncodeToString(h.Sum(nil))
	}
	return s, nil
}

// Marshal renders "<hex>  <name>\n" lines sorted by name.
func (s Sums) Marshal() []byte {
	names := make([]string, 0, len(s))
	for n := range s {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", s[n], n)
	}
	return b.Bytes()
}

// Parse reads sha256sum output. Blank lines are ignored; the binary marker
// ("<hex> *name") is accepted.
func Parse(b []byte) (Sums, error) {
	s := Sums{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 67 || !isHex64(line[:64]) || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') || line[66:] == "" {
			return nil, fmt.Errorf("SHA256SUMS line %d: malformed", n)
		}
		s[line[66:]] = strings.ToLower(line[:64])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func isHex64(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

// Verify streams r through SHA-256 and compares it with the digest listed for name.
func (s Sums) Verify(name string, r io.Reader) error {
	want, ok := s[name]
	if !ok {
		return fmt.Errorf("%s: not listed in SHA256SUMS", name)
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return fmt.Errorf("%s: read: %w", name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: checksum mismatch (want %s, got %s)", name, want, got)
	}
	return nil
}
