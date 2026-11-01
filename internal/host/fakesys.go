package host

import (
	"context"
	"fmt"
	"strings"
)

// FakeSys is an in-memory Sys for tests. Every call is appended to Calls as
// a readable string such as "systemctl enable tw" or "CreateUser tw /var/lib/tw".
type FakeSys struct {
	Calls  []string
	Active map[string]bool // unit -> active; set by start/restart/stop, read by IsActive
	// VerifyErr is returned by VerifyUnit; RestartErr by "systemctl restart".
	VerifyErr, RestartErr error
	Users                 map[string]bool // consulted by UserExists; CreateUser adds, DeleteUser removes
	// ActiveAfterRestart, when set, is what IsActive returns (it is called on
	// every IsActive, so it can look at the files on disk) and decides the
	// Active entry after "systemctl start" or "restart". When nil, IsActive
	// reads Active and start/restart make the unit active.
	ActiveAfterRestart func(unit string) bool
	// ReloadErr is returned by DaemonReload; EnableErr by "systemctl enable".
	ReloadErr, EnableErr error
	// Journal is returned by JournalTail.
	Journal string
	// Owners maps a path to its "user:group"; Chown sets it, Owner reads it
	// (a path never chowned has owner and group "").
	Owners map[string]string
	// Fail makes a call fail: a call whose recorded string (see Calls) is a
	// key returns that error and has no effect. Owner, which is not recorded,
	// is looked up as "Owner <path>".
	Fail map[string]error
}

var _ Sys = (*FakeSys)(nil)

// record appends the call to Calls and returns its Fail error, if any.
func (f *FakeSys) record(format string, a ...any) error {
	call := fmt.Sprintf(format, a...)
	f.Calls = append(f.Calls, call)
	return f.Fail[call]
}

func (f *FakeSys) DaemonReload(context.Context) error {
	if err := f.record("DaemonReload"); err != nil {
		return err
	}
	return f.ReloadErr
}

func (f *FakeSys) Systemctl(_ context.Context, args ...string) error {
	if err := f.record("systemctl %s", strings.Join(args, " ")); err != nil {
		return err
	}
	if len(args) < 2 {
		return nil
	}
	if f.Active == nil {
		f.Active = map[string]bool{}
	}
	verb, unit := args[0], args[len(args)-1]
	switch verb {
	case "enable":
		return f.EnableErr
	case "restart":
		if f.RestartErr != nil {
			return f.RestartErr
		}
		fallthrough
	case "start":
		active := true
		if f.ActiveAfterRestart != nil {
			active = f.ActiveAfterRestart(unit)
		}
		f.Active[unit] = active
	case "stop":
		f.Active[unit] = false
	}
	return nil
}

func (f *FakeSys) IsActive(_ context.Context, unit string) bool {
	_ = f.record("IsActive %s", unit)
	if f.ActiveAfterRestart != nil {
		return f.ActiveAfterRestart(unit)
	}
	return f.Active[unit]
}

func (f *FakeSys) JournalTail(_ context.Context, unit string, n int) string {
	_ = f.record("JournalTail %s %d", unit, n)
	return f.Journal
}

func (f *FakeSys) VerifyUnit(_ context.Context, path string) error {
	if err := f.record("VerifyUnit %s", path); err != nil {
		return err
	}
	return f.VerifyErr
}

func (f *FakeSys) UserExists(name string) bool { return f.Users[name] }

func (f *FakeSys) CreateUser(_ context.Context, name, home string) error {
	if err := f.record("CreateUser %s %s", name, home); err != nil {
		return err
	}
	if f.Users == nil {
		f.Users = map[string]bool{}
	}
	f.Users[name] = true
	return nil
}

func (f *FakeSys) DeleteUser(_ context.Context, name string) error {
	if err := f.record("DeleteUser %s", name); err != nil {
		return err
	}
	delete(f.Users, name)
	return nil
}

func (f *FakeSys) Chown(path, owner string) error {
	if err := f.record("Chown %s %s", path, owner); err != nil {
		return err
	}
	if f.Owners == nil {
		f.Owners = map[string]string{}
	}
	u, g, hasGroup := strings.Cut(owner, ":")
	if !hasGroup {
		_, g, _ = strings.Cut(f.Owners[path], ":")
	}
	f.Owners[path] = u + ":" + g
	return nil
}

func (f *FakeSys) Owner(path string) (string, string, error) {
	if err := f.Fail["Owner "+path]; err != nil {
		return "", "", err
	}
	u, g, _ := strings.Cut(f.Owners[path], ":")
	return u, g, nil
}

func (f *FakeSys) CreateTmpfiles(_ context.Context, conf string) error {
	return f.record("CreateTmpfiles %s", conf)
}
