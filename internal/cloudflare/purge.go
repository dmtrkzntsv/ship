// Package cloudflare purges the Cloudflare cache of a zone.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultBaseURL is the Cloudflare API v4 root.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Client talks to the Cloudflare API with a bearer token. An empty BaseURL
// means DefaultBaseURL; a nil HTTP means http.DefaultClient.
type Client struct {
	BaseURL, Token string
	HTTP           *http.Client
}

// PurgeEverything purges the whole cache of zone. The API answers 200 with
// {"success":false,...} for auth and permission problems, so the body is
// checked as well as the status.
func (c Client) PurgeEverything(ctx context.Context, zone string) error {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	endpoint := strings.TrimRight(base, "/") + "/zones/" + url.PathEscape(zone) + "/purge_cache"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(`{"purge_everything":true}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare purge request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("Cloudflare purge: reading response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Cloudflare purge failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var r struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &r); err != nil || !r.Success {
		return fmt.Errorf("Cloudflare purge rejected: %s", strings.TrimSpace(string(body)))
	}
	return nil
}
