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

// reservedServices are names install refuses as a service: system
// users and top-level directories a service's user, /opt/S, /etc/S or
// /var/lib/S would collide with.
var reservedServices = map[string]bool{
	"root": true, "ship": true, "systemd": true, "ssl": true, "ssh": true, "sshd": true,
	"nobody": true, "daemon": true, "bin": true, "sys": true, "etc": true, "opt": true,
	"var": true, "usr": true, "home": true, "tmp": true, "run": true, "proc": true,
	"dev": true, "boot": true, "lib": true,
}

// ReservedService reports whether name may not be used as a service name.
func ReservedService(name string) bool { return reservedServices[name] }

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
	if v == "." || v == ".." {
		return fmt.Errorf("invalid version %q", v)
	}
	if !versionRe.MatchString(v) {
		return fmt.Errorf("invalid version %q: allowed characters are A-Z a-z 0-9 . _ -", v)
	}
	return nil
}

// ValidateChannel accepts a moving channel (latest, dev) or a valid version.
func ValidateChannel(c string) error {
	if IsMoving(c) {
		return nil
	}
	return ValidateVersion(c)
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
