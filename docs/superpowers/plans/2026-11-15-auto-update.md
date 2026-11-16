# Automatic Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Opted-in installs that follow a moving channel update themselves from a systemd timer, and automatic updates notify one configured channel.

**Architecture:** A per-ref `auto_update` flag in `state.json`; a ship-owned `ship-auto-update.timer` runs `ship update --auto`, which walks opted-in refs through the existing update path. `release` and `host` return typed errors so the run can tell a broken build, a failed rollback and an unreachable store apart; outcomes go to a copied `internal/notify` sender.

**Tech Stack:** Go (stdlib + existing cobra/testify/BurntSushi toml), systemd.

**Spec:** `docs/superpowers/specs/2026-11-15-auto-update-design.md`

## Global Constraints

- Off by default; only refs with `auto_update` on are touched by `ship update --auto`.
- Timer: `OnBootSec=2min`, `OnUnitActiveSec=5min`, `RandomizedDelaySec=1min`; service `Type=oneshot`, `ExecStart=<ship path> update --auto`.
- Notifications only from `ship update --auto`; manual commands never notify.
- "Can't check" grace: 1 hour; reminder every 24 hours; recovery message after one was sent.
- Every message starts with `os.Hostname()` + `": "`.
- Notify send: up to 3 attempts within ~30 s on `notify.ErrTemporary`; failure logged, run exits non-zero, update kept.
- `state.json` (0644) holds timestamps only, never error text or the notify URL. Run clock file `/var/lib/ship/auto-update.json` is 0600.
- No new third-party dependency. Coverage of `internal/...` stays ≥ 90% (`make cover`).
- Conventional commits; squash-merge (owner preference).

## Review Focus

1. A ref with auto-update on is pinned with `ship update --version 1.2.3` → refused with a message telling to pass `--auto-update=false` (Task 4 test `TestUpdatePinWithAutoUpdateRefused`).
2. Two refs, the first's build is broken → the second still updates, run exits non-zero, one "Failed" message (Task 6 `TestAutoUpdateContinuesAfterFailure`).
3. SIGTERM during an automatic update's health check → previous pair restored, no `skip`, no message (Task 6 `TestAutoUpdateInterruptNotSkipped`).
4. Notify URL unset → automatic updates still run and nothing is sent (Task 6 `TestAutoUpdateWithoutNotifyURL`).
5. A build `skip`ped, then the same build republished under the same version with a new publish time → it is a different build and is tried (Task 6 `TestAutoUpdateSkipMatchesPublishTime`).

---

### Task 1: Copy the notify package and configure its URL

**Files:**
- Create: `internal/notify/{notify,telegram,ntfy,slack,discord,generic,smtp}.go`, `internal/notify/notify_test.go` (copied)
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `config.example.toml`

**Interfaces:**
- Produces: package `notify` exactly as twillingate's (`notify.Parse(raw string) (notify.Sender, error)`, `notify.Message{Title, Body, Link}`, `ErrInvalidURL`, `ErrPermanent`, `ErrTemporary`); `config.Config.Notify.URL string` (toml `[notify] url`, env `SHIP_NOTIFY_URL`).

- [ ] **Step 1: Copy** the eight files of `github.com/dmtrkzntsv/twillingate` `internal/shared/notify` at commit `7b735ed1312bbe0422ae563a789a155b9eb7df5e` into `internal/notify` (`gh api repos/dmtrkzntsv/twillingate/contents/internal/shared/notify/<f>?ref=<sha> --jq .content | base64 -d`). Change nothing but add to the package comment in `notify.go`: `// Copied from github.com/dmtrkzntsv/twillingate internal/shared/notify at 7b735ed; keep the two in step.`
- [ ] **Step 2: Run** `go test ./internal/notify/` — Expected: PASS.
- [ ] **Step 3: Write failing tests** in `config_test.go`: `TestLoadNotifyURLFromFile` (`[notify]\nurl = "ntfy://ntfy.sh/x"` → `cfg.Notify.URL == "ntfy://ntfy.sh/x"`) and `TestLoadNotifyURLEnvOverrides` (`SHIP_NOTIFY_URL=telegram://t:k@telegram?chats=1` wins over the file).
- [ ] **Step 4: Run** `go test ./internal/config/` — Expected: FAIL (no field `Notify`).
- [ ] **Step 5: Implement** `type Notify struct{ URL string \`toml:"url"\` }`, field `Notify Notify \`toml:"notify"\``, `override(&cfg.Notify.URL, "SHIP_NOTIFY_URL")`. Add to `config.example.toml`:
  ```toml
  [notify]
  url = ""   # automatic update notifications, e.g. telegram://<token>@telegram?chats=<chat_id>
  ```
- [ ] **Step 6: Run** `go test ./internal/config/ ./internal/notify/` — Expected: PASS.
- [ ] **Step 7: Commit** `feat(notify): copy twillingate's notification senders, add [notify] url`

### Task 2: Typed failures from release and host

**Files:**
- Modify: `internal/release/fetch.go`, `internal/release/errors_test.go`, `internal/host/update.go`, `internal/host/update_test.go`

**Interfaces:**
- Produces: `release.ErrBadBuild` (sentinel); `host.ErrBadBuild`, `host.ErrRolledBack`, `host.ErrRevertFailed` (sentinels). Existing messages unchanged (wrap with `%w` additionally; use `errors.Join` or `fmt.Errorf("%w: …")` so `errors.Is` works and the text stays as today).

Classification (spec "Failed builds and rollback"):
- `release.ErrBadBuild`: `no asset`, `no linux/<arch> build`, any `sums.Verify` failure (checksum mismatch, not listed), `release.json` naming another project, malformed `SHA256SUMS`, malformed `release.json`. NOT: `channel %q not found`, store errors.
- `host.ErrBadBuild`: `renderVerified` failures (template error, `does not verify`).
- `host.ErrRolledBack`: `revert` succeeded (returned "… previous version restored").
- `host.ErrRevertFailed`: every other `revert` return, plus a new case: after the revert's restart, `h.WaitHealthy` on the old pair fails → `"%s failed and the previous version did not come back up (%v): %w"`.
- Interruption keeps `context.Canceled` in the chain (already true via `WaitHealthy`); add it with `%w` if a path loses it.

- [ ] **Step 1: Write failing tests**: `TestFetchBadBuildErrors` (table: checksum mismatch, no arch, no asset, wrong project → `errors.Is(err, release.ErrBadBuild)`), `TestFetchChannelNotFoundIsNotBadBuild`, `TestFetchStoreErrorIsNotBadBuild`; host: `TestUpdateBadUnitIsBadBuild`, `TestUpdateHealthFailIsRolledBack`, `TestUpdateRevertFailIsRevertFailed` (make the restore fail), `TestUpdateOldPairNotHealthyIsRevertFailed` (FakeSys reports inactive after revert), `TestUpdateInterruptedKeepsCanceled`.
- [ ] **Step 2: Run** `go test ./internal/release/ ./internal/host/` — Expected: FAIL.
- [ ] **Step 3: Implement** the sentinels and wrapping as classified above; revert now calls `h.WaitHealthy(ctx, in.unit)` after its restart (ctx is already `WithoutCancel`). Rollback's revert gets the same check (fine for manual rollback too).
- [ ] **Step 4: Run** `go test ./internal/...` — Expected: PASS (existing tests unchanged; if a FakeSys default makes the new post-revert health check fail an old test, adjust the fake, not the assertion).
- [ ] **Step 5: Commit** `feat(update): typed failures for broken builds and failed rollbacks`

### Task 3: The auto-update timer units

**Files:**
- Create: `internal/host/autoupdate.go`, `internal/host/autoupdate_test.go`
- Modify: `internal/host/paths.go`

**Interfaces:**
- Produces: `const host.AutoUpdateUnit = "ship-auto-update"`; `func (p Paths) TimerPath(unit string) string` (`/etc/systemd/system/<unit>.timer`); `func (h *Host) EnsureAutoUpdateTimer(ctx context.Context, shipPath string) error`.

- [ ] **Step 1: Write failing tests**: `TestEnsureAutoUpdateTimerWritesUnits` asserts the two files' exact content (below, with `shipPath=/usr/local/bin/ship`), mode 0644, and `e.sys.Calls` ends with `daemon-reload` then `systemctl enable --now ship-auto-update.timer`; `TestEnsureAutoUpdateTimerIdempotent` (second call with same content makes no `daemon-reload`, still runs `enable --now`).
  ```ini
  # ship-auto-update.service
  [Unit]
  Description=ship automatic updates
  After=network-online.target
  Wants=network-online.target

  [Service]
  Type=oneshot
  ExecStart=/usr/local/bin/ship update --auto
  ```
  ```ini
  # ship-auto-update.timer
  [Unit]
  Description=Run ship automatic updates every 5 minutes

  [Timer]
  OnBootSec=2min
  OnUnitActiveSec=5min
  RandomizedDelaySec=1min

  [Install]
  WantedBy=timers.target
  ```
  (Files start at `[Unit]`; the `#` lines above are labels, not content.)
- [ ] **Step 2: Run** `go test ./internal/host/ -run AutoUpdateTimer` — Expected: FAIL.
- [ ] **Step 3: Implement** with `writeFileAtomic`; compare existing content to skip the reload.
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(host): ship-auto-update timer and service units`

### Task 4: The auto_update setting, skip, and status

**Files:**
- Modify: `internal/state/state.go`, `internal/app/install.go`, `internal/app/update.go`, `internal/app/ops.go` (Status), `internal/app/app.go`, tests in `internal/app/{install,update,app}_test.go`, `internal/state/state_test.go`

**Interfaces:**
- Consumes: `host.EnsureAutoUpdateTimer`, `notify.Parse`, `config.Config.Notify.URL`.
- Produces:
  - `state.RefState` new fields: `AutoUpdate bool \`json:"auto_update,omitempty"\``, `SkipVersion string \`json:"skip_version,omitempty"\``, `SkipPublishedAt time.Time \`json:"skip_published_at,omitzero"\``, `CheckFailingSince time.Time \`json:"check_failing_since,omitzero"\``, `CheckNotifiedAt time.Time \`json:"check_notified_at,omitzero"\``.
  - `InstallArgs.AutoUpdate bool`; `UpdateArgs.AutoUpdate *bool` (nil = unchanged).
  - `App.ShipPath func() (string, error)` (production: `os.Executable` then `filepath.EvalSymlinks`).
  - `func (a *App) enableAutoUpdate(ctx context.Context) error`: validates `a.Cfg.Notify.URL` with `notify.Parse` when non-empty (error returned as is), then `a.Host.EnsureAutoUpdateTimer(ctx, path)`.
  - `func isMoving(channel string, rel release.Release) bool` = `layout.BuiltinChannel(channel) || rel.Channel == channel`.

Rules:
- Turning on (`install --auto-update`, or update with `AutoUpdate=&true`) when the fetched release is not moving → `fmt.Errorf("%s follows version %s, which never changes; auto-update needs a moving channel (latest, dev, edge, …)", unit, channel)`, nothing changed. Validate the notify URL before touching the host.
- An update that changes the channel to a non-moving one while `AutoUpdate` stays on → same refusal, plus `"; pass --auto-update=false to pin it"`.
- Turning on calls `enableAutoUpdate` after the state save succeeds; if it fails, the command fails with the error (state keeps the flag; the next `--auto-update` retries).
- Manual `Update` clears `SkipVersion`/`SkipPublishedAt` whenever it runs past the lock, including the "already on" path.
- Manual `Rollback` on a ref with `AutoUpdate` sets `SkipVersion`/`SkipPublishedAt` to the version/publish time it rolled away from.
- Turning auto-update off clears `CheckFailingSince`/`CheckNotifiedAt`.
- `Status` header becomes `SERVICE  REF  UNIT  CHANNEL  VERSION  AUTO  STATE`; AUTO is `on` or `-`.

- [ ] **Step 1: Write failing tests**: `TestInstallAutoUpdate` (flag saved, `systemctl enable --now ship-auto-update.timer` in calls, timer file exists under root), `TestInstallAutoUpdateOnVersionRefused` (state unchanged, no unit written), `TestInstallAutoUpdateInvalidNotifyURL` (`Cfg.Notify.URL="bogus://x"` → error contains `invalid notification url`, nothing installed), `TestUpdateTurnsAutoUpdateOnAndOff`, `TestUpdatePinWithAutoUpdateRefused`, `TestUpdateClearsSkip`, `TestRollbackSetsSkipWhenAuto`, `TestRollbackNoSkipWhenManual`, `TestStatusShowsAuto` (exact table output).
- [ ] **Step 2: Run** `go test ./internal/app/ ./internal/state/` — Expected: FAIL.
- [ ] **Step 3: Implement** per the rules above; `newEnv` in tests sets `ShipPath` to return `/usr/local/bin/ship`.
- [ ] **Step 4: Run** `go test ./internal/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): per-ref auto_update setting, skip on rollback, AUTO in status`

### Task 5: Notifier with retry and the run clock file

**Files:**
- Create: `internal/app/notifier.go`, `internal/app/notifier_test.go`, `internal/app/runclock.go`, `internal/app/runclock_test.go`

**Interfaces:**
- Produces:
  - `type Notifier interface{ Notify(ctx context.Context, m notify.Message) error }`
  - `func newNotifier(sender notify.Sender, host string, sleep func(time.Duration)) Notifier` — prefixes `m.Title` (or `m.Body` when no title) with `host + ": "`; retries on `notify.ErrTemporary` up to 3 attempts with sleeps 5s, 15s; returns the last error. A nil sender yields a no-op Notifier.
  - `App.Notifier func() (Notifier, error)` — production builds from `a.Cfg.Notify.URL` (`notify.Parse`, `os.Hostname`, `time.Sleep`); empty URL → no-op.
  - `type runClock struct{ FailingSince, NotifiedAt time.Time; Reason string }` with `loadRunClock(path string) (runClock, error)` (missing → zero) and `(c runClock) save(path string) error` (0600, atomic). Default path `const runClockPath = "/var/lib/ship/auto-update.json"`; `App.RunClockPath string`.
  - `type failClock struct{ Since, Notified time.Time }` and `func (c *failClock) observe(now time.Time, failing bool) clockEvent` with `clockEvent` one of `clockNone, clockFailing, clockStillFailing, clockRecovered`:
    - failing, Since zero → set Since=now, `clockNone`
    - failing, Notified zero, now−Since ≥ 1h → Notified=now, `clockFailing`
    - failing, Notified set, now−Notified ≥ 24h → Notified=now, `clockStillFailing`
    - not failing, Notified set → reset, `clockRecovered`; not failing, Notified zero → reset, `clockNone`
  Used for both the per-ref clock (mapped onto `RefState.CheckFailingSince/CheckNotifiedAt`) and the run clock.

- [ ] **Step 1: Write failing tests**: `TestNotifierPrefixesHost`, `TestNotifierRetriesTemporary` (fake sender fails twice with ErrTemporary then succeeds; sleeps recorded `[5s 15s]`), `TestNotifierStopsOnPermanent` (1 attempt), `TestNotifierNilSenderNoop`, `TestFailClockSequence` (table over the five rows above, including 59m → none, 60m → failing, +23h → none, +24h → still failing, success → recovered), `TestRunClockRoundTripMode0600`, `TestRunClockMissingIsZero`.
- [ ] **Step 2: Run** `go test ./internal/app/ -run 'Notifier|Clock'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(update): notifier with retry and failure clocks`

### Task 6: `App.AutoUpdate`, the run

**Files:**
- Create: `internal/app/autoupdate.go`, `internal/app/autoupdate_test.go`
- Modify: `internal/app/update.go` (extract the shared update core)

**Interfaces:**
- Consumes: everything above; `a.purgeCache`, `a.checkToken`.
- Produces: `func (a *App) AutoUpdate(ctx context.Context) error`.

Refactor first: move the body of `Update` after locking/loading into `func (a *App) updateRef(ctx context.Context, s *state.State, st store.Store, ref layout.Ref, service string, rs *state.RefState, channel string, quiet bool) (changed bool, err error)` that both `Update` and `AutoUpdate` call; `quiet` suppresses the "already on" line. Existing `Update` tests must pass unchanged.

Run algorithm:
1. Build the Notifier (`a.Notifier()`); an invalid URL returns the parse error immediately (unit fails; nothing else to tell).
2. `a.State.Lock()`. If held: load the run clock, `observe(now, true)` with Reason `another ship run has held the state lock`, save it, notify per event, print `another ship run holds the state lock; trying again next run`, return nil. Any later run that gets the lock and reaches step 3's success resets the clock, so only an hour of consecutive locked runs alerts.
3. Load state, then `a.NewStore()`. Either failing → run clock `observe(now,true)` with the error text as Reason (kept in the 0600 file), notify per event, return the error. Success → run clock `observe(now,false)`; `clockRecovered` notifies.
4. For each service/ref (sorted by unit) with `AutoUpdate`: call `updateRef(..., quiet=true)` with a pre-check: before `host.Update`, if `f.Release.Version == rs.SkipVersion && f.Release.PublishedAt.Equal(rs.SkipPublishedAt)` → skip silently. (Implement the check inside `updateRef` via an optional `skip func(release.Release) bool` parameter or a field on a small args struct; builder's choice.)
5. Classify the result:
   - nil, changed → ref clock `observe(false)`; message Updated; if `svc.CFZone != ""` and `a.checkToken()==nil` purge (failure: print, mark run failed); zone set but no token → print `cache not purged: no Cloudflare token`.
   - nil, unchanged → ref clock `observe(false)` (may send recovery).
   - `release.ErrBadBuild`, `host.ErrBadBuild`, `host.ErrRolledBack` (and not `context.Canceled`) → set Skip to the fetched build; message Failed; run failed.
   - `host.ErrRevertFailed` → set Skip; message Down; run failed.
   - `context.Canceled` in chain → no skip, no message; stop the loop and return the error.
   - anything else (channel not found, store/network) → ref clock `observe(true)`; message per event; run failed.
6. Save state after each ref. Print one line per change or failure (`<unit>: <old> -> <new>`, `<unit>: <error>`). Return an error summarizing how many refs failed (or a notify failure) so the unit exits non-zero.

Messages (`notify.Message.Title`, `Body` = details; host prefix added by Notifier):
- Updated: Title `<unit> (<channel>) <old> → <new>` (use `republished`-style publish time when versions are equal).
- Failed: Title `<unit> (<channel>) build <ver> failed, rolled back to <old>`, Body = the error text (carries the log lines).
- Down: Title `<unit> is DOWN — update to <ver> failed and rollback to <old> failed`, Body = error text.
- Can't check: Title `can't check <unit> (<channel>) for updates since <15:04>`, Body = error; still: `still can't check … (<dur>)`; recovered: `<unit> (<channel>) reachable again, was failing since <15:04> (<dur>)`.
- Run clock: `automatic updates can't run since <15:04>` / `still can't run … (<dur>)` / `automatic updates running again, were failing since <15:04> (<dur>)`, Body = Reason.
Times in the host's local zone; durations rounded to minutes (`3h12m`).

- [ ] **Step 1: Write failing tests** (fake Notifier recording messages; `a.Now` stepped): `TestAutoUpdateUpdatesOptedInOnly`, `TestAutoUpdateQuietWhenNothingChanged` (no output, no message), `TestAutoUpdateBrokenBuildSkippedOnce` (checksum mismatch → Failed once; second run: no restart, no message), `TestAutoUpdateHealthFailSkipped`, `TestAutoUpdateRevertFailedSendsDown` (auto_update stays on, skip set), `TestAutoUpdateSkipMatchesPublishTime`, `TestAutoUpdateContinuesAfterFailure`, `TestAutoUpdateInterruptNotSkipped`, `TestAutoUpdateCantCheckAfterOneHour` (store errors at t0, t0+30m → nothing; t0+60m → can't check; t0+24h → nothing; t0+84h → still; success → recovered), `TestAutoUpdateChannelNotFoundIsCantCheck`, `TestAutoUpdateLockHeld` (returns nil, prints message; after >1h of held runs → run message), `TestAutoUpdateStateUnreadable` (garbage state.json → run clock, after 1h a message), `TestAutoUpdatePurgesWithZoneAndToken`, `TestAutoUpdateNoTokenSkipsPurge`, `TestAutoUpdateNotifyFailureExitsNonZero` (update kept in state), `TestAutoUpdateWithoutNotifyURL`, `TestAutoUpdateStateHoldsNoErrorText` (after a can't-check run, state.json contains no error substring).
- [ ] **Step 2: Run** `go test ./internal/app/ -run AutoUpdate` — Expected: FAIL.
- [ ] **Step 3: Refactor** `Update` into `updateRef`; run `go test ./internal/app/` — Expected: existing tests PASS.
- [ ] **Step 4: Implement** `AutoUpdate`.
- [ ] **Step 5: Run** `go test ./internal/...` — Expected: PASS.
- [ ] **Step 6: Commit** `feat(update): ship update --auto runs opted-in updates and notifies`

### Task 7: CLI flags, e2e, docs

**Files:**
- Modify: `internal/cli/update.go`, `internal/cli/install.go`, `internal/cli/*_test.go`, `test/e2e/e2e.sh`, `README.md`, `docs/superpowers/specs/2026-10-24-ship-design.md`

**Interfaces:**
- Consumes: `InstallArgs.AutoUpdate`, `UpdateArgs.AutoUpdate`, `App.AutoUpdate`.

CLI:
- `install --auto-update` (bool).
- `update --auto-update[=true|false]`: pass `&v` only when `cmd.Flags().Changed("auto-update")`.
- `update --auto`: takes no `<ref>` and no other flag (`cobra.MaximumNArgs(1)`; with `--auto` exactly 0 args and none of `--service --version --cf-zone --purge-cache --auto-update` changed, else error `--auto takes no ref and no other flags`); without `--auto`, exactly 1 arg as today. Needs root/Linux like `update`.

- [ ] **Step 1: Write failing CLI tests**: `TestUpdateAutoRejectsRef`, `TestUpdateAutoRejectsOtherFlags`, `TestUpdateAutoUpdateFlagUnsetPassesNil`, `TestInstallAutoUpdateFlag` (follow the existing cli test style in `commands_test.go`).
- [ ] **Step 2: Run** `go test ./internal/cli/` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/cli/` — Expected: PASS.
- [ ] **Step 5: e2e**: in `test/e2e/e2e.sh` add a section after the existing update checks: install an e2e ref on `dev` with `--auto-update`; assert `/etc/systemd/system/ship-auto-update.timer` exists and `systemctl is-enabled ship-auto-update.timer` is `enabled`; publish a new dev build; run `ship update --auto`; `assert_running` the new version; `st` shows `auto_update == true`. Add `ship-auto-update.{service,timer}` to `cleanup` (disable --now, remove, daemon-reload) and to `preflight`'s must-not-exist list. Run `shellcheck -S warning test/e2e/e2e.sh` locally; the e2e itself runs in CI.
- [ ] **Step 6: Docs**: README — add `--auto-update` to the `install`/`update` lines and `ship update --auto` to Commands; new section "Automatic updates" (opt-in, moving channels only, the timer and changing its interval with `systemctl edit ship-auto-update.timer`, skip and how a manual update clears it, the five messages, `[notify] url` / `SHIP_NOTIFY_URL` with a Telegram example, journal: `journalctl -u ship-auto-update`). Main design spec: a short "Automatic updates" section pointing to `2026-11-15-auto-update-design.md`.
- [ ] **Step 7: Run** `make lint cover` — Expected: PASS, coverage ≥ 90%.
- [ ] **Step 8: Commit** `feat(cli): --auto-update and update --auto; docs and e2e`
