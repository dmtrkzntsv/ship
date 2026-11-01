package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), mode))
	require.NoError(t, os.Chmod(p, mode))
	return p
}

const fileBody = `[r2]
endpoint = "https://acc.example.com"
access_key_id = "kid"
secret_access_key = "sec"
bucket = "lab"

[cloudflare]
api_token = "tok"
`

func TestLoadFirstExistingFile(t *testing.T) {
	dir := t.TempDir()
	second := writeFile(t, dir, "b.toml", fileBody, 0o600)
	cfg, used, warns, err := Load(env(nil), []string{filepath.Join(dir, "missing.toml"), second})
	require.NoError(t, err)
	assert.Equal(t, second, used)
	assert.Empty(t, warns)
	assert.Equal(t, "https://acc.example.com", cfg.R2.Endpoint)
	assert.Equal(t, "kid", cfg.R2.AccessKeyID)
	assert.Equal(t, "sec", cfg.R2.SecretAccessKey)
	assert.Equal(t, "tok", cfg.Cloudflare.APIToken)
}

func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.toml", fileBody, 0o600)
	cfg, _, _, err := Load(env(map[string]string{
		"SHIP_R2_BUCKET":    "other",
		"SHIP_CF_API_TOKEN": "envtok",
	}), []string{p})
	require.NoError(t, err)
	assert.Equal(t, "other", cfg.R2.Bucket)
	assert.Equal(t, "envtok", cfg.Cloudflare.APIToken)
	assert.Equal(t, "kid", cfg.R2.AccessKeyID)
}

func TestDefaultBucket(t *testing.T) {
	cfg, _, _, err := Load(env(nil), nil)
	require.NoError(t, err)
	assert.Equal(t, "ship", cfg.R2.Bucket)
}

func TestNoFileEnvOnly(t *testing.T) {
	cfg, used, warns, err := Load(env(map[string]string{
		"SHIP_R2_ENDPOINT":          "https://e",
		"SHIP_R2_ACCESS_KEY_ID":     "k",
		"SHIP_R2_SECRET_ACCESS_KEY": "s",
	}), []string{"/nonexistent/ship.toml"})
	require.NoError(t, err)
	assert.Equal(t, "", used)
	assert.Empty(t, warns)
	assert.NoError(t, cfg.R2.Validate())
}

func TestLooseModeWarns(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "d.toml", fileBody, 0o644)
	_, used, warns, err := Load(env(nil), []string{p})
	require.NoError(t, err)
	assert.Equal(t, p, used)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], "chmod 600")
	assert.Contains(t, warns[0], p)
}

func TestValidateNamesVariable(t *testing.T) {
	full := R2{Endpoint: "e", AccessKeyID: "k", SecretAccessKey: "s"}
	assert.NoError(t, full.Validate())

	r := full
	r.Endpoint = ""
	assert.ErrorContains(t, r.Validate(), "SHIP_R2_ENDPOINT")
	r = full
	r.AccessKeyID = ""
	assert.ErrorContains(t, r.Validate(), "SHIP_R2_ACCESS_KEY_ID")
	r = full
	r.SecretAccessKey = ""
	assert.ErrorContains(t, r.Validate(), "SHIP_R2_SECRET_ACCESS_KEY")
}

func TestUnreadableFileSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}
	dir := t.TempDir()
	locked := writeFile(t, dir, "locked.toml", fileBody, 0o000)
	next := writeFile(t, dir, "next.toml", fileBody, 0o600)
	_, used, warns, err := Load(env(nil), []string{locked, next})
	require.NoError(t, err)
	assert.Equal(t, next, used)
	assert.Empty(t, warns)
}

func TestInvalidTOMLIsError(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "bad.toml", "[r2\n", 0o600)
	_, _, _, err := Load(env(nil), []string{p})
	assert.ErrorContains(t, err, p)
}

func TestDefaultPaths(t *testing.T) {
	assert.Equal(t, []string{SystemPath}, DefaultPaths("/home/u", true))
	assert.Equal(t, []string{"/home/u/.config/ship/config.toml", SystemPath}, DefaultPaths("/home/u", false))
}

func TestS3Options(t *testing.T) {
	o := R2{Endpoint: "e", AccessKeyID: "k", SecretAccessKey: "s", Bucket: "b"}.S3Options()
	assert.Equal(t, "e", o.Endpoint)
	assert.Equal(t, "k", o.AccessKeyID)
	assert.Equal(t, "s", o.SecretAccessKey)
	assert.Equal(t, "b", o.Bucket)
}

func TestReadErrorIsError(t *testing.T) {
	// A directory is neither missing nor permission-denied: Load must not skip it.
	dir := t.TempDir()
	next := writeFile(t, dir, "next.toml", fileBody, 0o600)
	_, _, _, err := Load(env(nil), []string{dir, next})
	assert.ErrorContains(t, err, "read "+dir)
}
