package release_test

import (
	"bytes"
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

func TestPublishFolderCheckStoreErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		version  string
		channels []string
		set      func(*faultyStore)
	}{
		"channel release.json": {"", []string{"edge"}, func(f *faultyStore) { f.getErr = map[string]error{"econumo/edge/release.json": errBoom} }},
		"channel SHA256SUMS":   {"", []string{"edge"}, func(f *faultyStore) { f.getErr = map[string]error{"econumo/edge/SHA256SUMS": errBoom} }},
		"version release.json": {"v1", nil, func(f *faultyStore) {
			f.Objects["econumo/v1/x"] = storetest.MemObject{Data: []byte("x")}
			f.getErr = map[string]error{"econumo/v1/release.json": errBoom}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &faultyStore{Mem: storetest.NewMem()}
			tc.set(f)
			o := opts(fixture(t))
			o.Version, o.Channels = tc.version, tc.channels
			assert.ErrorIs(t, release.Publish(context.Background(), f, o), errBoom)
			assert.Empty(t, f.PutOrder)
		})
	}
}

// replaceReleaseJSON swaps release.json for data and lists it in SHA256SUMS,
// so only its content is wrong.
func replaceReleaseJSON(t *testing.T, st *storetest.Mem, project string, data []byte) {
	t.Helper()
	key := project + "/latest/"
	o := st.Objects[key+"release.json"]
	o.Data = data
	st.Objects[key+"release.json"] = o
	sm, err := sumsOf(st.Objects[key+"SHA256SUMS"].Data)
	require.NoError(t, err)
	sm["release.json"] = hexSum(data)
	so := st.Objects[key+"SHA256SUMS"]
	so.Data = sm.Marshal()
	st.Objects[key+"SHA256SUMS"] = so
}

func TestFetchBadBuildErrors(t *testing.T) {
	econumo := func(t *testing.T) *storetest.Mem {
		return publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
	}
	for _, tc := range []struct {
		name string
		arch string
		ref  layout.Ref
		want string // part of the original message that must survive
		set  func(t *testing.T) *storetest.Mem
	}{
		{"checksum mismatch", "amd64", layout.Ref{Project: "econumo"}, "checksum mismatch", func(t *testing.T) *storetest.Mem {
			st := econumo(t)
			o := st.Objects["econumo/latest/econumo-linux-amd64"]
			o.Data = append([]byte("x"), o.Data...)
			st.Objects["econumo/latest/econumo-linux-amd64"] = o
			return st
		}},
		{"tampered release.json", "amd64", layout.Ref{Project: "econumo"}, "checksum mismatch", func(t *testing.T) *storetest.Mem {
			st := econumo(t)
			o := st.Objects["econumo/latest/release.json"]
			o.Data = append([]byte(" "), o.Data...)
			st.Objects["econumo/latest/release.json"] = o
			return st
		}},
		{"no arch", "arm", layout.Ref{Project: "econumo"}, "no linux/arm build", econumo},
		{"no asset", "amd64", layout.Ref{Project: "econumo", Component: "agent"}, "no asset \"econumo-agent\"", econumo},
		{"wrong project", "amd64", layout.Ref{Project: "econumo"}, "names project \"other\"", func(t *testing.T) *storetest.Mem {
			st := econumo(t)
			replaceReleaseJSON(t, st, "econumo", []byte(`{"project":"other","version":"v1","published_at":"2026-10-29T12:00:00Z"}`))
			return st
		}},
		{"malformed release.json", "amd64", layout.Ref{Project: "econumo"}, "release.json", func(t *testing.T) *storetest.Mem {
			st := econumo(t)
			replaceReleaseJSON(t, st, "econumo", []byte("not json"))
			return st
		}},
		{"malformed SHA256SUMS", "amd64", layout.Ref{Project: "econumo"}, "malformed", func(t *testing.T) *storetest.Mem {
			st := econumo(t)
			o := st.Objects["econumo/latest/SHA256SUMS"]
			o.Data = []byte("garbage\n")
			st.Objects["econumo/latest/SHA256SUMS"] = o
			return st
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := release.Fetch(context.Background(), tc.set(t), tc.ref, "latest", tc.arch, dir)
			require.Error(t, err)
			assert.ErrorIs(t, err, release.ErrBadBuild)
			assert.ErrorContains(t, err, tc.want, "the original text is kept")
			assert.Empty(t, dirFiles(t, dir))
		})
	}
}

func TestFetchFailureKeepsRelease(t *testing.T) {
	st := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
	want := st.Objects["econumo/latest/release.json"]
	o := st.Objects["econumo/latest/econumo-linux-amd64"]
	o.Data = append([]byte("x"), o.Data...)
	st.Objects["econumo/latest/econumo-linux-amd64"] = o

	f, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "amd64", t.TempDir())
	require.ErrorIs(t, err, release.ErrBadBuild)
	assert.NotEmpty(t, f.Release.Version)
	assert.Contains(t, string(want.Data), f.Release.Version)
	assert.Empty(t, f.Binary)
}

func TestFetchChannelNotFoundIsNotBadBuild(t *testing.T) {
	st := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64")
	_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "v9", "amd64", t.TempDir())
	require.Error(t, err)
	assert.NotErrorIs(t, err, release.ErrBadBuild)
}

func TestFetchStoreErrorIsNotBadBuild(t *testing.T) {
	files := []string{"econumo-linux-amd64", "econumo-linux-arm64", "econumo.service"}
	for name, set := range map[string]func(*faultyStore){
		"get SHA256SUMS":     func(f *faultyStore) { f.getErr = map[string]error{"econumo/latest/SHA256SUMS": errBoom} },
		"get release.json":   func(f *faultyStore) { f.getErr = map[string]error{"econumo/latest/release.json": errBoom} },
		"read release.json":  func(f *faultyStore) { f.badBody = map[string]error{"econumo/latest/release.json": errBoom} },
		"get binary":         func(f *faultyStore) { f.getErr = map[string]error{"econumo/latest/econumo-linux-amd64": errBoom} },
		"read binary":        func(f *faultyStore) { f.badBody = map[string]error{"econumo/latest/econumo-linux-amd64": errBoom} },
		"read unit mid-copy": func(f *faultyStore) { f.badBody = map[string]error{"econumo/latest/econumo.service": errBoom} },
	} {
		t.Run(name, func(t *testing.T) {
			f := &faultyStore{Mem: publishFixture(t, "econumo", files...)}
			set(f)
			dir := t.TempDir()
			_, err := release.Fetch(context.Background(), f, layout.Ref{Project: "econumo"}, "latest", "amd64", dir)
			require.ErrorIs(t, err, errBoom)
			assert.NotErrorIs(t, err, release.ErrBadBuild)
			assert.Empty(t, dirFiles(t, dir))
		})
	}
}

// publishingStore is a Mem whose SHA256SUMS reads after the first return
// later (a publish landed mid-fetch), or fail with laterErr.
type publishingStore struct {
	*storetest.Mem
	key      string
	later    []byte
	laterErr error
	reads    int
}

func (p *publishingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == p.key {
		p.reads++
		if p.reads > 1 {
			if p.laterErr != nil {
				return nil, p.laterErr
			}
			return io.NopCloser(bytes.NewReader(p.later)), nil
		}
	}
	return p.Mem.Get(ctx, key)
}

func TestFetchSumsChangedDuringDownloadIsNotBadBuild(t *testing.T) {
	for _, tamper := range []string{"econumo/latest/econumo-linux-amd64", "econumo/latest/release.json", "econumo/latest/econumo.service"} {
		t.Run(filepath.Base(tamper), func(t *testing.T) {
			mem := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
			o := mem.Objects[tamper]
			o.Data = append([]byte("new "), o.Data...)
			mem.Objects[tamper] = o
			st := &publishingStore{Mem: mem, key: "econumo/latest/SHA256SUMS", later: []byte("the new sums\n")}
			dir := t.TempDir()

			_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "amd64", dir)
			require.EqualError(t, err, "econumo/latest/SHA256SUMS changed during the download; try again")
			assert.NotErrorIs(t, err, release.ErrBadBuild)
			assert.Empty(t, dirFiles(t, dir))
		})
	}
}

func TestFetchSumsRereadFailsIsNotBadBuild(t *testing.T) {
	mem := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
	o := mem.Objects["econumo/latest/econumo-linux-amd64"]
	o.Data = append([]byte("x"), o.Data...)
	mem.Objects["econumo/latest/econumo-linux-amd64"] = o
	st := &publishingStore{Mem: mem, key: "econumo/latest/SHA256SUMS", laterErr: errBoom}

	_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "amd64", t.TempDir())
	require.ErrorIs(t, err, errBoom)
	assert.NotErrorIs(t, err, release.ErrBadBuild)
	assert.Contains(t, err.Error(), "checksum mismatch")
}

func TestFetchSumsUnchangedMismatchIsBadBuild(t *testing.T) {
	mem := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
	o := mem.Objects["econumo/latest/econumo-linux-amd64"]
	o.Data = append([]byte("x"), o.Data...)
	mem.Objects["econumo/latest/econumo-linux-amd64"] = o
	st := &publishingStore{Mem: mem, key: "econumo/latest/SHA256SUMS", later: mem.Objects["econumo/latest/SHA256SUMS"].Data}

	_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "amd64", t.TempDir())
	require.ErrorIs(t, err, release.ErrBadBuild)
	assert.Equal(t, 2, st.reads, "SHA256SUMS was read again to tell")
}

func TestFetchMismatchCarriesSumsHash(t *testing.T) {
	for _, tamper := range []string{"econumo/latest/econumo-linux-amd64", "econumo/latest/release.json", "econumo/latest/econumo.service"} {
		t.Run(filepath.Base(tamper), func(t *testing.T) {
			st := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
			o := st.Objects[tamper]
			o.Data = append([]byte("x"), o.Data...)
			st.Objects[tamper] = o
			_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "amd64", t.TempDir())
			require.ErrorIs(t, err, release.ErrMismatch)
			require.ErrorIs(t, err, release.ErrBadBuild)
			var mm *release.MismatchError
			require.ErrorAs(t, err, &mm)
			assert.Equal(t, hexSum(st.Objects["econumo/latest/SHA256SUMS"].Data), mm.SumsHash)
			assert.Contains(t, err.Error(), "checksum mismatch")
		})
	}
}

func TestFetchStructuralBadBuildIsNotMismatch(t *testing.T) {
	st := publishFixture(t, "econumo", "econumo-linux-amd64", "econumo-linux-arm64", "econumo.service")
	_, err := release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "arm", t.TempDir())
	require.ErrorIs(t, err, release.ErrBadBuild)
	assert.NotErrorIs(t, err, release.ErrMismatch)
	replaceReleaseJSON(t, st, "econumo", []byte(`{"project":"other","version":"v1"}`))
	_, err = release.Fetch(context.Background(), st, layout.Ref{Project: "econumo"}, "latest", "amd64", t.TempDir())
	require.ErrorIs(t, err, release.ErrBadBuild)
	assert.NotErrorIs(t, err, release.ErrMismatch)
}
