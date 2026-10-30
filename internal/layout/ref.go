// Package layout holds the pure naming rules shared by every ship command:
// project refs, channels, file and unit names, unit templates and the
// publish-directory scan.
package layout

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	versionRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Ref is a project ref: "<project>" or "<project>/<component>".
type Ref struct{ Project, Component string }

// ValidName reports whether s is a valid project, component or service name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// IsMoving reports whether channel is a moving channel (latest or dev).
func IsMoving(channel string) bool { return channel == "latest" || channel == "dev" }

// ValidateVersion checks that v is usable as an immutable version channel.
func ValidateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("version must not be empty")
	}
	if IsMoving(v) {
		return fmt.Errorf("version %q is a moving channel name", v)
	}
	if !versionRe.MatchString(v) {
		return fmt.Errorf("invalid version %q: allowed characters are A-Z a-z 0-9 . _ -", v)
	}
	return nil
}

// ParseRef parses "<project>" or "<project>/<component>".
func ParseRef(s string) (Ref, error) {
	parts := strings.Split(s, "/")
	if len(parts) > 2 {
		return Ref{}, fmt.Errorf("invalid ref %q: want <project> or <project>/<component>", s)
	}
	for _, p := range parts {
		if !ValidName(p) {
			return Ref{}, fmt.Errorf("invalid ref %q: %q is not a valid name (lowercase letters, digits, - and _)", s, p)
		}
	}
	r := Ref{Project: parts[0]}
	if len(parts) == 2 {
		r.Component = parts[1]
	}
	return r, nil
}

func (r Ref) String() string {
	if r.Component == "" {
		return r.Project
	}
	return r.Project + "/" + r.Component
}

// Name is the component if present, else the project.
func (r Ref) Name() string {
	if r.Component != "" {
		return r.Component
	}
	return r.Project
}

// Asset is the file-name prefix used in the bucket.
func (r Ref) Asset() string {
	if r.Component != "" {
		return r.Project + "-" + r.Component
	}
	return r.Project
}

// DefaultService is the service a ref installs into unless overridden.
func (r Ref) DefaultService() string { return r.Project }
