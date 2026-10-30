// Package config loads ship's settings from a TOML file and environment variables.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/dmtrkzntsv/ship/internal/store"
)

// SystemPath is the host-wide config file.
const SystemPath = "/etc/ship/config.toml"

const defaultBucket = "lab"

type Config struct {
	R2         R2         `toml:"r2"`
	Cloudflare Cloudflare `toml:"cloudflare"`
}

type R2 struct {
	Endpoint        string `toml:"endpoint"`
	AccessKeyID     string `toml:"access_key_id"`
	SecretAccessKey string `toml:"secret_access_key"`
	Bucket          string `toml:"bucket"`
}

type Cloudflare struct {
	APIToken string `toml:"api_token"`
}

// DefaultPaths returns the config lookup order: the user file first (non-root), then the system file.
func DefaultPaths(home string, root bool) []string {
	if root {
		return []string{SystemPath}
	}
	return []string{home + "/.config/ship/config.toml", SystemPath}
}

// Load reads the first existing file in paths (unreadable-by-permission files are skipped),
// then lets non-empty environment variables override each field, then applies defaults.
// used is the file read, or "" if none.
func Load(getenv func(string) string, paths []string) (cfg Config, used string, warnings []string, err error) {
	for _, p := range paths {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			if errors.Is(rerr, fs.ErrNotExist) || errors.Is(rerr, fs.ErrPermission) {
				continue
			}
			return Config{}, "", nil, fmt.Errorf("read %s: %w", p, rerr)
		}
		if _, derr := toml.Decode(string(data), &cfg); derr != nil {
			return Config{}, "", nil, fmt.Errorf("parse %s: %w", p, derr)
		}
		used = p
		if fi, serr := os.Stat(p); serr == nil && fi.Mode().Perm()&0o077 != 0 {
			warnings = append(warnings, fmt.Sprintf("%s is accessible by group/others (mode %04o); run: chmod 600 %s", p, fi.Mode().Perm(), p))
		}
		break
	}

	override := func(dst *string, name string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	override(&cfg.R2.Endpoint, "SHIP_R2_ENDPOINT")
	override(&cfg.R2.AccessKeyID, "SHIP_R2_ACCESS_KEY_ID")
	override(&cfg.R2.SecretAccessKey, "SHIP_R2_SECRET_ACCESS_KEY")
	override(&cfg.R2.Bucket, "SHIP_R2_BUCKET")
	override(&cfg.Cloudflare.APIToken, "SHIP_CF_API_TOKEN")
	if cfg.R2.Bucket == "" {
		cfg.R2.Bucket = defaultBucket
	}
	return cfg, used, warnings, nil
}

// Validate checks the fields needed to talk to R2.
func (r R2) Validate() error {
	switch {
	case r.Endpoint == "":
		return errors.New("R2 endpoint is not set (config [r2] endpoint or SHIP_R2_ENDPOINT)")
	case r.AccessKeyID == "":
		return errors.New("R2 access key id is not set (config [r2] access_key_id or SHIP_R2_ACCESS_KEY_ID)")
	case r.SecretAccessKey == "":
		return errors.New("R2 secret access key is not set (config [r2] secret_access_key or SHIP_R2_SECRET_ACCESS_KEY)")
	}
	return nil
}

func (r R2) S3Options() store.S3Options {
	return store.S3Options{Endpoint: r.Endpoint, AccessKeyID: r.AccessKeyID, SecretAccessKey: r.SecretAccessKey, Bucket: r.Bucket}
}
