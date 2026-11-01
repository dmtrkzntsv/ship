package release_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/layout"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/dmtrkzntsv/ship/internal/store/storetest"
)

// faultyStore is a Mem whose reads can fail for chosen prefixes and keys.
type faultyStore struct {
	*storetest.Mem
	dirsErr  map[string]error    // prefix -> ListDirs error
	listErr  map[string]error    // prefix -> List error
	getErr   map[string]error    // key -> Get error
	badBody  map[string]error    // key -> error reading the body Get returns
	extraDir map[string][]string // prefix -> extra (empty) dirs ListDirs reports
}

func (f *faultyStore) ListDirs(ctx context.Context, prefix string) ([]string, error) {
	if err := f.dirsErr[prefix]; err != nil {
		return nil, err
	}
	dirs, err := f.Mem.ListDirs(ctx, prefix)
	return append(dirs, f.extraDir[prefix]...), err
}

func (f *faultyStore) List(ctx context.Context, prefix string) ([]store.Object, error) {
	if err := f.listErr[prefix]; err != nil {
		return nil, err
	}
	return f.Mem.List(ctx, prefix)
}

func (f *faultyStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := f.getErr[key]; err != nil {
		return nil, err
	}
	if err := f.badBody[key]; err != nil {
		return io.NopCloser(iotest.ErrReader(err)), nil
	}
	return f.Mem.Get(ctx, key)
}

var errBoom = errors.New("boom")

func TestVersionsStoreErrors(t *testing.T) {
	for name, set := range map[string]func(*faultyStore){
		"list dirs":    func(f *faultyStore) { f.dirsErr = map[string]error{"econumo/": errBoom} },
		"list version": func(f *faultyStore) { f.listErr = map[string]error{"econumo/v3/": errBoom} },
		"get release":  func(f *faultyStore) { f.getErr = map[string]error{"econumo/v3/release.json": errBoom} },
		"read release": func(f *faultyStore) { f.badBody = map[string]error{"econumo/v3/release.json": errBoom} },
	} {
		t.Run(name, func(t *testing.T) {
			f := &faultyStore{Mem: prunable(t)}
			set(f)
			_, err := release.Versions(context.Background(), f, "econumo")
			assert.ErrorIs(t, err, errBoom)
			_, err = release.Prune(context.Background(), f, release.PruneOptions{Project: "econumo", Keep: 1})
			assert.ErrorIs(t, err, errBoom, "prune reports it too")
		})
	}
}

func TestVersionsSkipsEmptyFolders(t *testing.T) {
	f := &faultyStore{Mem: prunable(t), extraDir: map[string][]string{"econumo/": {"v99"}}}
	vs, err := release.Versions(context.Background(), f, "econumo")
	require.NoError(t, err)
	assert.NotContains(t, names(vs), "v99")
	assert.Len(t, vs, 15)
}

func TestPruneValidatesOptions(t *testing.T) {
	m := prunable(t)
	_, err := release.Prune(context.Background(), m, release.PruneOptions{Project: "Bad", Keep: 1})
	require.EqualError(t, err, `invalid project name "Bad"`)
	_, err = release.Prune(context.Background(), m, release.PruneOptions{Project: "econumo", Keep: -1})
	require.EqualError(t, err, "keep must be >= 0")
	assert.Len(t, m.Objects, 15*3, "nothing deleted")
}

func TestFetchStoreErrors(t *testing.T) {
	ref, err := layout.ParseRef("winterflow/hub")
	require.NoError(t, err)
	const p = "winterflow/latest/"
	for name, tc := range map[string]struct {
		set  func(*faultyStore)
		want string
	}{
		"get sums":    {func(f *faultyStore) { f.getErr = map[string]error{p + "SHA256SUMS": errBoom} }, "boom"},
		"get release": {func(f *faultyStore) { f.getErr = map[string]error{p + "release.json": errBoom} }, "release.json: boom"},
		"get binary":  {func(f *faultyStore) { f.getErr = map[string]error{p + "winterflow-hub-linux-amd64": errBoom} }, "winterflow-hub-linux-amd64: boom"},
		"get env":     {func(f *faultyStore) { f.getErr = map[string]error{p + "winterflow-hub.env.example": errBoom} }, "winterflow-hub.env.example: boom"},
	} {
		t.Run(name, func(t *testing.T) {
			f := &faultyStore{Mem: publishFixture(t, "winterflow", "winterflow-hub-linux-amd64", "winterflow-hub-linux-arm64", "winterflow-hub.service", "winterflow-hub.env.example")}
			tc.set(f)
			dir := t.TempDir()
			_, err := release.Fetch(context.Background(), f, ref, "latest", "amd64", dir)
			require.ErrorIs(t, err, errBoom)
			assert.Equal(t, tc.want, err.Error())
			assert.Empty(t, dirFiles(t, dir), "files written before the failure are removed")
		})
	}
}

func TestFetchMalformedSums(t *testing.T) {
	m := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64")
	m.Objects["econumo/latest/SHA256SUMS"] = storetest.MemObject{Data: []byte("garbage\n")}
	_, err := release.Fetch(context.Background(), m, layout.Ref{Project: "econumo"}, "latest", "amd64", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "econumo/latest/SHA256SUMS: ")
}

func TestFetchUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	m := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64")
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := release.Fetch(context.Background(), m, layout.Ref{Project: "econumo"}, "latest", "amd64", dir)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.Contains(t, err.Error(), filepath.Join(dir, "econumo-linux-amd64"))
}
