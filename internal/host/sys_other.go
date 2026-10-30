//go:build !linux

package host

import (
	"context"
	"errors"
)

// NewSys returns a Sys whose every operation fails: host commands are Linux only.
func NewSys() Sys { return otherSys{} }

type otherSys struct{}

var errLinuxOnly = errors.New("Linux only")

func (otherSys) DaemonReload(context.Context) error               { return errLinuxOnly }
func (otherSys) Systemctl(context.Context, ...string) error       { return errLinuxOnly }
func (otherSys) IsActive(context.Context, string) bool            { return false }
func (otherSys) JournalTail(context.Context, string, int) string  { return "" }
func (otherSys) VerifyUnit(context.Context, string) error         { return errLinuxOnly }
func (otherSys) UserExists(string) bool                           { return false }
func (otherSys) CreateUser(context.Context, string, string) error { return errLinuxOnly }
func (otherSys) DeleteUser(context.Context, string) error         { return errLinuxOnly }
func (otherSys) Chown(string, string) error                       { return errLinuxOnly }
func (otherSys) CreateTmpfiles(context.Context, string) error     { return errLinuxOnly }
