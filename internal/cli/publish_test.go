package cli

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishRejectsInvalidProject(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"publish", "Bad Name", "--path", t.TempDir(), "--channels", "latest"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid project name")
}

func TestPublishRequiresPath(t *testing.T) {
	root := NewRoot(io.Discard, io.Discard)
	root.SetArgs([]string{"publish", "econumo"})
	assert.Error(t, root.Execute())
}
