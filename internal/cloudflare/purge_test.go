package cloudflare_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

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

// roundTrip lets a test stand in for the network.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPurgeDefaultsToCloudflareAPI(t *testing.T) {
	var got string
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		got = r.URL.String()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`)), Request: r}, nil
	})}
	c := cloudflare.Client{Token: "t", HTTP: hc}
	require.NoError(t, c.PurgeEverything(context.Background(), "zone/1"))
	assert.Equal(t, cloudflare.DefaultBaseURL+"/zones/zone%2F1/purge_cache", got)
}

func TestPurgeNilHTTPUsesDefaultClient(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := cloudflare.Client{BaseURL: srv.URL + "/", Token: "t"}
	require.NoError(t, c.PurgeEverything(context.Background(), "z1"))
	assert.Equal(t, "/zones/z1/purge_cache", path, "a trailing slash on BaseURL is trimmed")
}

func TestPurgeTransportErrors(t *testing.T) {
	t.Run("bad base URL", func(t *testing.T) {
		c := cloudflare.Client{BaseURL: "http://[::1", Token: "t"}
		assert.Error(t, c.PurgeEverything(context.Background(), "z1"))
	})
	t.Run("request fails", func(t *testing.T) {
		hc := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		})}
		err := cloudflare.Client{BaseURL: "http://cf.invalid", Token: "t", HTTP: hc}.PurgeEverything(context.Background(), "z1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Cloudflare purge request failed")
		assert.Contains(t, err.Error(), "connection refused")
	})
	t.Run("body read fails", func(t *testing.T) {
		hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(errors.New("reset"))), Request: r}, nil
		})}
		err := cloudflare.Client{BaseURL: "http://cf.invalid", Token: "t", HTTP: hc}.PurgeEverything(context.Background(), "z1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Cloudflare purge: reading response: reset")
	})
	t.Run("not JSON", func(t *testing.T) {
		hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("<html>")), Request: r}, nil
		})}
		err := cloudflare.Client{BaseURL: "http://cf.invalid", Token: "t", HTTP: hc}.PurgeEverything(context.Background(), "z1")
		require.EqualError(t, err, "Cloudflare purge rejected: <html>")
	})
}
