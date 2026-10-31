package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/app"
	"github.com/dmtrkzntsv/ship/internal/cloudflare"
)

// cfServer is a fake Cloudflare API recording purge calls; reply is the
// body it answers with.
type cfServer struct {
	mu    sync.Mutex
	paths []string
	auths []string
	// sysCalls is the number of FakeSys calls seen when each purge arrived.
	sysCalls []int
	reply    string
}

// withCF installs a fake Cloudflare API and an API token on the app.
func withCF(t *testing.T, e *env) *cfServer {
	t.Helper()
	cf := &cfServer{reply: `{"success":true}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cf.mu.Lock()
		defer cf.mu.Unlock()
		cf.paths = append(cf.paths, r.URL.Path)
		cf.auths = append(cf.auths, r.Header.Get("Authorization"))
		cf.sysCalls = append(cf.sysCalls, len(e.sys.Calls))
		_, _ = w.Write([]byte(cf.reply))
	}))
	t.Cleanup(srv.Close)
	e.app.Cfg.Cloudflare.APIToken = "tok"
	e.app.CF = func(token string) *cloudflare.Client {
		return &cloudflare.Client{BaseURL: srv.URL, Token: token, HTTP: srv.Client()}
	}
	return cf
}

// zonedEnv is installedEnv with zone z1 on econumo-beta.
func zonedEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	require.NoError(t, e.app.Install(context.Background(), app.InstallArgs{Ref: "econumo", Service: "econumo-beta", Version: "dev", CFZone: "z1"}))
	publish(t, e.store, "econumo", "1.5.0", []string{"dev"}, true, "econumo")
	e.out.Reset()
	e.sys.Calls = nil
	e.app.Now = func() time.Time { return later }
	return e
}

func TestUpdatePurgeAfterSuccess(t *testing.T) {
	e := zonedEnv(t)
	cf := withCF(t, e)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true}))

	assert.Equal(t, []string{"/zones/z1/purge_cache"}, cf.paths)
	assert.Equal(t, []string{"Bearer tok"}, cf.auths)
	restart := -1
	for i, c := range e.sys.Calls {
		if c == "systemctl restart econumo-beta" {
			restart = i
		}
	}
	require.GreaterOrEqual(t, restart, 0)
	assert.Greater(t, cf.sysCalls[0], restart, "purge must come after the restart")
	assert.Equal(t, "1.5.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
}

func TestUpdateNoPurgeWithoutFlag(t *testing.T) {
	e := zonedEnv(t)
	cf := withCF(t, e)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	assert.Empty(t, cf.paths)
}

func TestUpdateNoPurgeOnRollback(t *testing.T) {
	e := zonedEnv(t)
	cf := withCF(t, e)
	e.sys.ActiveAfterRestart = func(string) bool { return false }
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
	require.Error(t, err)
	assert.Empty(t, cf.paths)
}

func TestUpdateNoPurgeWhenAlreadyOn(t *testing.T) {
	e := zonedEnv(t)
	cf := withCF(t, e)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true}))
	assert.Contains(t, e.out.String(), "already on 1.5.0")
	assert.Empty(t, cf.paths)
}

func TestUpdatePurgeFailureReportsSuccessfulUpdate(t *testing.T) {
	e := zonedEnv(t)
	cf := withCF(t, e)
	cf.reply = `{"success":false,"errors":[{"message":"nope"}]}`
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "succeeded (not rolled back)")
	assert.Contains(t, err.Error(), "cache purge failed")
	assert.Contains(t, err.Error(), "nope")
	assert.Equal(t, "1.5.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
}

func TestPurgeNeedsZoneUpFront(t *testing.T) {
	e := installedEnv(t) // installed without a zone
	cf := withCF(t, e)
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
	require.Error(t, err)
	assert.Equal(t, "--purge-cache: service econumo-beta has no Cloudflare zone; pass --cf-zone", err.Error())
	assert.Empty(t, e.sys.Calls)
	assert.Empty(t, cf.paths)
	assert.Equal(t, "1.4.0", e.state(t).Services["econumo-beta"].Refs["econumo"].Version)
}

func TestPurgeZoneFromArgsSatisfiesCheck(t *testing.T) {
	e := installedEnv(t)
	cf := withCF(t, e)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true, CFZone: "z9"}))
	assert.Equal(t, []string{"/zones/z9/purge_cache"}, cf.paths)
}

func TestPurgeNeedsToken(t *testing.T) {
	e := zonedEnv(t)
	cf := withCF(t, e)
	e.app.Cfg.Cloudflare.APIToken = ""
	err := e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "SHIP_CF_API_TOKEN") && strings.Contains(err.Error(), "api_token"), err.Error())
	assert.Empty(t, e.sys.Calls)
	assert.Empty(t, cf.paths)

	err = e.app.Uninstall(context.Background(), app.UninstallArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SHIP_CF_API_TOKEN")
	assert.Empty(t, e.sys.Calls)
	assert.Contains(t, e.state(t).Services, "econumo-beta")
}

func TestUpdateCFZoneStored(t *testing.T) {
	e := zonedEnv(t)
	require.NoError(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", CFZone: "z2"}))
	assert.Equal(t, "z2", e.state(t).Services["econumo-beta"].CFZone)
}

func TestUpdateCFZoneStoredWhenAlreadyOn(t *testing.T) {
	e := zonedEnv(t)
	ctx := context.Background()
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta"}))
	require.NoError(t, e.app.Update(ctx, app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", CFZone: "z3"}))
	assert.Equal(t, "z3", e.state(t).Services["econumo-beta"].CFZone)
}

func TestUpdateCFZoneNotStoredOnFailure(t *testing.T) {
	e := zonedEnv(t)
	e.sys.ActiveAfterRestart = func(string) bool { return false }
	require.Error(t, e.app.Update(context.Background(), app.UpdateArgs{Ref: "econumo", Service: "econumo-beta", CFZone: "z2"}))
	assert.Equal(t, "z1", e.state(t).Services["econumo-beta"].CFZone)
}

func TestUninstallPurgeLastRefUsesStoredZone(t *testing.T) {
	e := zonedEnv(t)
	cf := withCF(t, e)
	require.NoError(t, e.app.Uninstall(context.Background(), app.UninstallArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true}))
	assert.NotContains(t, e.state(t).Services, "econumo-beta")
	assert.Equal(t, []string{"/zones/z1/purge_cache"}, cf.paths)
}

func TestUninstallPurgeNeedsZoneUpFront(t *testing.T) {
	e := installedEnv(t)
	cf := withCF(t, e)
	err := e.app.Uninstall(context.Background(), app.UninstallArgs{Ref: "econumo", Service: "econumo-beta", PurgeCache: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no Cloudflare zone")
	assert.Empty(t, e.sys.Calls)
	assert.Empty(t, cf.paths)
	assert.Contains(t, e.state(t).Services, "econumo-beta")
}
