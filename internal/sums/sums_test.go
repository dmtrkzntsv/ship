package sums

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hexOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestComputeMarshalParseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b-bin"), []byte("bbb"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-bin"), []byte("aaa"), 0o644))

	s, err := Compute(dir, []string{"b-bin", "a-bin"})
	require.NoError(t, err)
	assert.Equal(t, Sums{"a-bin": hexOf("aaa"), "b-bin": hexOf("bbb")}, s)

	out := s.Marshal()
	want := hexOf("aaa") + "  a-bin\n" + hexOf("bbb") + "  b-bin\n"
	assert.Equal(t, want, string(out))

	back, err := Parse(out)
	require.NoError(t, err)
	assert.Equal(t, s, back)
}

func TestComputeMissingFile(t *testing.T) {
	_, err := Compute(t.TempDir(), []string{"nope"})
	require.Error(t, err)
}

func TestParseBinaryMarker(t *testing.T) {
	s, err := Parse([]byte(hexOf("x") + " *econumo-linux-amd64\n"))
	require.NoError(t, err)
	assert.Equal(t, Sums{"econumo-linux-amd64": hexOf("x")}, s)
}

func TestParseBlankLinesAndMalformed(t *testing.T) {
	s, err := Parse([]byte("\n" + hexOf("x") + "  f\n\n"))
	require.NoError(t, err)
	assert.Len(t, s, 1)

	for _, bad := range []string{"garbage", hexOf("x") + " f", "abc  f", hexOf("x") + "  "} {
		_, err := Parse([]byte("\n" + bad + "\n"))
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "line 2", bad)
	}
}

func TestParseDuplicateName(t *testing.T) {
	_, err := Parse([]byte(hexOf("a") + "  f\n" + hexOf("b") + "  f\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 2")
	assert.Contains(t, err.Error(), "duplicate")
}

func TestVerify(t *testing.T) {
	const name = "econumo-linux-amd64"
	s := Sums{name: hexOf("content")}
	assert.NoError(t, s.Verify(name, strings.NewReader("content")))

	err := s.Verify(name, strings.NewReader("changed"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), name+": checksum mismatch")

	err = s.Verify("econumo-darwin-arm64", strings.NewReader("content"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "econumo-darwin-arm64: not listed")
}
