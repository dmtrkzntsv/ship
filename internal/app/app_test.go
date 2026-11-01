package app_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/release"
	"github.com/dmtrkzntsv/ship/internal/state"
)

// shipEnv points HOME at a temp dir holding config (when non-empty) and
// clears the SHIP_* variables, so New reads only what the test sets up.
func shipEnv(t *testing.T, config string, mode os.FileMode) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"SHIP_R2_ENDPOINT", "SHIP_R2_ACCESS_KEY_ID", "SHIP_R2_SECRET_ACCESS_KEY", "SHIP_R2_BUCKET", "SHIP_CF_API_TOKEN"} {
		t.Setenv(k, "")
	}
	if config == "" {
		return
	}
	p := filepath.Join(home, ".config", "ship", "config.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, []byte(config), mode))
	require.NoError(t, os.Chmod(p, mode))
}

func TestNewWiresHost(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads only the system config")
	}
	shipEnv(t, "[r2]\nendpoint = \"http://r2.invalid\"\naccess_key_id = \"AK\"\nsecret_access_key = \"SK\"\n[cloudflare]\napi_token = \"cf\"\n", 0o644)
	t.Setenv("SHIP_R2_BUCKET", "bkt")
	var out, errOut bytes.Buffer

	a, err := app.New(&out, &errOut)
	require.NoError(t, err)

	assert.Contains(t, errOut.String(), "warning: ")
	assert.Contains(t, errOut.String(), "chmod 600")
	assert.Empty(t, out.String())
	assert.Equal(t, "http://r2.invalid", a.Cfg.R2.Endpoint)
	assert.Equal(t, "bkt", a.Cfg.R2.Bucket)
	assert.Equal(t, "cf", a.Cfg.Cloudflare.APIToken)
	assert.Same(t, &out, a.Out)
	assert.Same(t, &errOut, a.Err)
	assert.Equal(t, state.File{Path: state.DefaultPath}, a.State)
	assert.Equal(t, release.HostArch(), a.Arch)
	require.NotNil(t, a.Host)
	assert.Equal(t, "", a.Host.Paths.Root)
	assert.NotNil(t, a.Host.Sys)
	assert.Same(t, &out, a.Host.Out)
	assert.Equal(t, 10*time.Second, a.Host.HealthWait)
	assert.Equal(t, time.Second, a.Host.HealthTick)
	assert.WithinDuration(t, time.Now(), a.Now(), time.Minute)

	cf := a.CF("tok")
	assert.Equal(t, "tok", cf.Token)
	assert.Empty(t, cf.BaseURL)
	require.NotNil(t, cf.HTTP)
	assert.Equal(t, 30*time.Second, cf.HTTP.Timeout)

	st, err := a.NewStore()
	require.NoError(t, err)
	assert.NotNil(t, st)
}

func TestNewStoreValidatesConfigWhenCalled(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads only the system config")
	}
	shipEnv(t, "[r2]\naccess_key_id = \"AK\"\nsecret_access_key = \"SK\"\n", 0o600)
	var errOut bytes.Buffer
	a, err := app.New(&bytes.Buffer{}, &errOut)
	require.NoError(t, err, "a missing endpoint is not an error until the store is needed")
	assert.Empty(t, errOut.String(), "a 0600 config draws no warning")

	_, err = a.NewStore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "R2 endpoint is not set")

	// The store reads the config at call time, so a fixed config works.
	a.Cfg.R2.Endpoint = "http://r2.invalid"
	_, err = a.NewStore()
	assert.NoError(t, err)
}

func TestNewConfigError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads only the system config")
	}
	shipEnv(t, "[r2\nendpoint = ", 0o600)
	a, err := app.New(&bytes.Buffer{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Nil(t, a)
	assert.Contains(t, err.Error(), "parse ")
	assert.Contains(t, err.Error(), "config.toml")
}
