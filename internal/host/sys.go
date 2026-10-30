package host

import "context"

// Sys is the host's systemd and user-management surface. The real
// implementation shells out to system tools; FakeSys records calls.
type Sys interface {
	DaemonReload(ctx context.Context) error
	Systemctl(ctx context.Context, args ...string) error
	IsActive(ctx context.Context, unit string) bool
	JournalTail(ctx context.Context, unit string, n int) string
	VerifyUnit(ctx context.Context, path string) error
	UserExists(name string) bool
	CreateUser(ctx context.Context, name, home string) error
	DeleteUser(ctx context.Context, name string) error
	Chown(path, owner string) error // owner is "user" or "user:group"
	// Owner returns the user and group names owning path (following symlinks).
	Owner(path string) (user, group string, err error)
	CreateTmpfiles(ctx context.Context, conf string) error
}
