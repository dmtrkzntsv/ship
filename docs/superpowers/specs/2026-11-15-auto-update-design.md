# Automatic updates

## Intent

When a project publishes a new build, the hosts that run it pick it up on
their own, without anyone running `ship update`. It suits dev/edge boxes that
should follow every build; production stays under manual control unless an
operator decides otherwise.

## Goals, non-goals, anti-goals

- Goal: an install that follows a moving channel (`latest`, `dev`, `edge`, …)
  and has auto-update on is updated within minutes of a publish, with the same
  checksum verification, health check and automatic rollback as `ship update`.
- Goal: off by default; turned on per install (owner's decision).
- Non-goal: updating the `ship` binary itself (`ship self-update` stays manual).
- Non-goal: instant delivery; a few minutes of delay is fine.
- Goal: a notification to one configured channel (Telegram for the owner)
  when an automatic update lands, fails, or cannot run.
- Anti-goal: a broken build making a service restart over and over.
- Anti-goal: a false alarm at 3am from one flaky fetch.
- Anti-goal: any new credential or inbound access on hosts; hosts keep only
  their read-only R2 key, CI keeps knowing nothing about hosts.

## Approaches

1. **systemd timer on each host** (picked). A timer runs `ship update --auto`
   every few minutes; it updates every opted-in install through the normal
   update path. Fits read-only hosts and systemd-for-everything; costs up to a
   few minutes of delay and a couple of R2 reads per install per run.
2. Long-running `ship` daemon polling R2. Same behaviour plus a process to keep
   alive and monitor; nothing the timer does not get from systemd.
3. Publish pushes to hosts (SSH/webhook from CI). Instant, but CI must know and
   hold credentials for every host and hosts must accept inbound calls, which
   breaks the current design.

## Design

**Turning it on.** `ship install … --auto-update` and
`ship update … --auto-update[=true|false]` set a per-ref `auto_update` flag in
`/var/lib/ship/state.json` (the update flag alone changes the setting, with or
without a new version). Refused on a ref pinned to an immutable version, which
never changes. `ship status` shows it per ref.

**The timer.** Turning auto-update on for any ref writes and enables
`ship-auto-update.service` (`Type=oneshot`,
`ExecStart=/usr/local/bin/ship update --auto`, path taken from the running
binary) and `ship-auto-update.timer` (`OnBootSec=2min`,
`OnUnitActiveSec=5min`, `RandomizedDelaySec=1min`). *My call:* the units stay
in place when the last ref turns auto-update off or is uninstalled; a run with
nothing to do exits quietly. An operator changes the interval with a systemd
drop-in.

**A run.** `ship update --auto` (root, no ref) walks every ref with
`auto_update` on and runs the existing update for it. Up-to-date refs print
nothing in this mode (no "already on" noise in the journal). One ref failing
does not stop the others; the run exits non-zero if any failed, so
`systemctl status ship-auto-update` shows the failure and the journal has the
details. *My call:* if another ship command holds the state lock, the run logs
that and exits 0; the next tick retries.

**Failed builds and rollback** (*my call*; this is the anti-goal above). When
an automatic update fails its health check and is rolled back, ship records
that build (version + publish time) as `skip` on the ref, and later runs leave
it alone until a different build is published. A manual `ship rollback` on an
auto-updating ref records the build it rolled back from the same way, so the
next tick does not undo the operator. A manual `ship update` ignores and clears
`skip`.

*My call (after the builder check):* a build that is broken before the unit is
touched is treated like a failed health check (`skip` + one "Failed"
message): checksum mismatch, no build for the host's arch, no asset,
`release.json` naming another project, no unit file, a unit that does not
verify. A missing channel, R2 or network errors and a rejected key are
"can't check" (below). `release` and `host` return typed errors so the run can
tell the two apart. A health check cut short by shutdown (SIGTERM during the
run) is not a failed build: the existing interrupt path restores the previous
pair and nothing is skipped or notified.

**Rollback fails too** (owner's decision: keep going). When the update fails
and restoring the previous binary and unit fails as well, or the previous
build does not come back up, ship sends an urgent "Down" message (below),
marks the build `skip`, and leaves auto-update on, so a later fixed build can
land on its own and bring the service back.

**Cloudflare cache.** *My call:* an automatic update that changed the version
purges the service's cache when the service has a stored zone and a CF token is
configured, as `--purge-cache` would; with no token it skips the purge and says
so. A purge failure keeps the update and makes the run exit non-zero.

## Notifications

The owner wants to hear about automatic updates on their phone, and only
about automatic updates: manual commands never notify (the operator is there).

**Configuration.** One optional URL, `[notify] url = "…"` in the config file
or `SHIP_NOTIFY_URL`, in shoutrrr's URL format (`telegram://`, `ntfy://`,
`slack://`, `discord://`, `generic://`, `smtp://`). Unset means no
notifications. An invalid URL makes `ship update --auto` fail at start with
the parse error (it never names the secret); setting `--auto-update` on a ref
while the URL is invalid is refused for the same reason. Documented in README
and `config.example.toml`.

**Sender code.** Copied unchanged, tests included, from twillingate's
`internal/shared/notify` into `internal/notify`, with a header comment naming
the source repo and commit (owner's decision: copy now, extract to a shared
module only once a third project needs it). Standard library only.

**Events.** Every message starts with the host name (`os.Hostname`).

| Event | When | Message (illustrative) |
| --- | --- | --- |
| Updated | an automatic update succeeded | `homelab: econumo (dev) 2026-11-15T10:02 → 2026-11-16T09:40` |
| Failed | an automatic update failed its health check and was rolled back | `homelab: econumo (dev) build X failed, rolled back to Y`, plus the last journal lines the health check already collects |
| Down | an automatic update failed and the rollback failed too | `homelab: econumo is DOWN — update to X failed and rollback to Y failed`, plus journal lines |
| Can't check | runs could not fetch a ref's release (R2 or network down, bad key) continuously for 1 hour | `homelab: can't check econumo (dev) for updates since 14:02: <error>` |
| Still can't check | every 24 hours while it continues | same, with the duration |
| Checking again | the first successful fetch after "can't check" was sent | `homelab: econumo (dev) reachable again, was failing since 14:02 (3h)` |

Runs where nothing changed never notify. A failed build notifies once: it is
then `skip`ped (see above), so later runs leave it alone silently. *My call:*
the "can't check" clock is per ref and kept in state (`check_failing_since`,
`check_notified_at`), so a ship restart or a reboot does not re-announce or
lose it; failures before the first hour notify nothing (no 3am false alarm
for one flaky fetch). A ref whose auto-update is turned off, or that is
uninstalled, drops its clock without a recovery message. `state.json` is mode
0644, so it holds only timestamps, never error text or anything from the
notify URL.

*My call (after the builder check):* failures that stop the whole run before
any ref is reached also notify, under the same 1-hour rule, 24-hour reminder
and recovery message: the state file cannot be read or parsed, the R2 settings
are missing or invalid, or the state lock has been held by another ship
process for over an hour (each locked run notes it and exits 0). Their clock
lives in its own file, `/var/lib/ship/auto-update.json` (0600), since the
state file may be the thing that is broken. The message names the reason:
`homelab: automatic updates can't run since 14:02: <error>`.

**Sending.** Synchronous, at the end of each ref's update, with a short retry
on temporary errors (a few attempts within ~30 s; the run is a oneshot, there
is no queue). A send that still fails is logged to the journal and makes the
run exit non-zero, but never undoes or blocks an update, and is not retried on
a later run. *My call.*

**Docs.** README gets an "Automatic updates" section; the main design spec
gets a short section pointing here.

## Left to the builder

Names of internal functions and the exact `status` column layout; test layout
follows the existing `app`/`host` fakes.
