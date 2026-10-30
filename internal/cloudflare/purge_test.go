package cloudflare_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/cloudflare"
)

func TestPurgeSuccess(t *testing.T) {
	var method, path, auth, ctype, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, auth, ctype, body = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	c := cloudflare.Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	require.NoError(t, c.PurgeEverything(context.Background(), "z1"))
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/zones/z1/purge_cache", path)
	assert.Equal(t, "Bearer t", auth)
	assert.Equal(t, "application/json", ctype)
	assert.JSONEq(t, `{"purge_everything":true}`, body)
}

func TestPurgeRejected(t *testing.T) {
	const reply = `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()

	c := cloudflare.Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	err := c.PurgeEverything(context.Background(), "z1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), reply)
}

func TestPurgeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := cloudflare.Client{BaseURL: srv.URL, Token: "t", HTTP: srv.Client()}
	err := c.PurgeEverything(context.Background(), "z1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}
