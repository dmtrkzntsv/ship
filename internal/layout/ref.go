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
	channelRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// reservedServices are names install refuses as a service: system
// users and top-level directories a service's user, /opt/S, /etc/S or
// /var/lib/S would collide with.
var reservedServices = map[string]bool{
	"root": true, "ship": true, "systemd": true, "ssl": true, "ssh": true, "sshd": true,
	"nobody": true, "daemon": true, "bin": true, "sys": true, "etc": true, "opt": true,
	"var": true, "usr": true, "home": true, "tmp": true, "run": true, "proc": true,
	"dev": true, "boot": true, "lib": true,
	AutoUpdateUnit: true,
}

// AutoUpdateUnit names ship's own systemd service and timer that run
// `ship update --auto`; no service or unit of a ref may take it.
const AutoUpdateUnit = "ship-auto-update"

// ReservedService reports whether name may not be used as a service name.
func ReservedService(name string) bool { return reservedServices[name] }

// Ref is a project ref: "<project>" or "<project>/<component>".
type Ref struct{ Project, Component string }

// ValidName reports whether s is a valid project, component or service name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// BuiltinChannel reports whether name is latest or dev. These are moving
// channels by name alone, also in buckets published before release.json
// carried the "channel" field that marks every other moving channel, and
// they can never be used as a version.
func BuiltinChannel(name string) bool { return name == "latest" || name == "dev" }

// ValidateChannelName checks a name given to publish --channels: a lowercase
// letter, then lowercase letters, digits, - and _. Starting with a letter and
// having no dot keeps channel names apart from version numbers like 1.2.3.
func ValidateChannelName(c string) error {
	if !channelRe.MatchString(c) {
		return fmt.Errorf("invalid channel name %q: use a lowercase letter, then lowercase letters, digits, - and _ (e.g. latest, dev, edge)", c)
	}
	return nil
}

// ValidateVersion checks that v is usable as an immutable version channel.
func ValidateVersion(v string) error {
	if v == "" {
		return fmt.Errorf("version must not be empty")
	}
	if BuiltinChannel(v) {
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

// ValidateChannel checks a name given to install, update or self-update
// --version: latest, dev, or any name valid as a version, which includes
// every valid moving channel name. Whether it is a moving channel or an
// immutable version is the bucket's business (see release.Release.Channel);
// a host fetches it the same way either way.
func ValidateChannel(c string) error {
	if BuiltinChannel(c) {
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
