// Package state models the on-host state file that records installed
// services, and provides atomic saving and an exclusive lock.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// DefaultPath is where the state file lives on a host.
const DefaultPath = "/var/lib/ship/state.json"

type State struct {
	Services    map[string]*Service `json:"services"`
	SelfChannel string              `json:"self_channel,omitempty"`
}

type Service struct {
	CFZone string `json:"cf_zone,omitempty"`
	// CreatedUser records that ship created the service's user, so
	// uninstalling the last ref with --purge may delete it. A user that existed before is kept.
	CreatedUser bool                 `json:"created_user,omitempty"`
	Refs        map[string]*RefState `json:"refs"` // key: Ref.String()
}

type RefState struct {
	Unit        string `json:"unit"`
	Channel     string `json:"channel"`
	Version     string `json:"version"`
	PrevVersion string `json:"prev_version,omitempty"`
	// PublishedAt is the release's publish time. A channel-only publish
	// records the channel name as its version, so the publish time is what
	// tells two builds of the same channel apart.
	PublishedAt     time.Time `json:"published_at,omitzero"`
	PrevPublishedAt time.Time `json:"prev_published_at,omitzero"`
	InstalledAt     time.Time `json:"installed_at"`
}

// File is a state file on disk; its lock file is state.lock in the same dir.
type File struct{ Path string }

func (f File) lockPath() string { return filepath.Join(filepath.Dir(f.Path), "state.lock") }

// Load reads the state file. A missing file yields an empty State.
func (f File) Load() (*State, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Services: map[string]*Service{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state %s: %w", f.Path, err)
	}
	s := &State{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", f.Path, err)
	}
	if s.Services == nil {
		s.Services = map[string]*Service{}
	}
	for name, svc := range s.Services {
		if svc == nil {
			svc = &Service{}
			s.Services[name] = svc
		}
		if svc.Refs == nil {
			svc.Refs = map[string]*RefState{}
		}
	}
	return s, nil
}

// Save writes the state atomically (temp file + rename) with mode 0644.
func (f File) Save(s *State) error {
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write state %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod state %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync state %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		return fmt.Errorf("replace state %s: %w", f.Path, err)
	}
	return nil
}

// Lock takes an exclusive, non-blocking flock on the lock file.
func (f File) Lock() (unlock func(), err error) {
	lp := f.lockPath()
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	fd, err := os.OpenFile(lp, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", lp, err)
	}
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fd.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another ship run holds %s", lp)
		}
		return nil, fmt.Errorf("lock %s: %w", lp, err)
	}
	return func() {
		_ = syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
		_ = fd.Close()
	}, nil
}

// Service returns the named service, creating it (with non-nil Refs) if absent.
func (s *State) Service(name string) *Service {
	if s.Services == nil {
		s.Services = map[string]*Service{}
	}
	svc := s.Services[name]
	if svc == nil {
		svc = &Service{}
		s.Services[name] = svc
	}
	if svc.Refs == nil {
		svc.Refs = map[string]*RefState{}
	}
	return svc
}

// Units returns the sorted unit names of a service; unknown service yields none.
func (s *State) Units(service string) []string {
	svc := s.Services[service]
	if svc == nil {
		return nil
	}
	units := make([]string, 0, len(svc.Refs))
	for _, r := range svc.Refs {
		units = append(units, r.Unit)
	}
	sort.Strings(units)
	return units
}
