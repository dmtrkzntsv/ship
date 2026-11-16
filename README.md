# ship

`ship` publishes a project's built files to a Cloudflare R2 bucket and installs
them on Linux hosts as hardened systemd services. One static Go binary does both
jobs:

- on a laptop or in CI: `ship publish` uploads a folder of binaries, unit
  templates and env examples as a versioned release;
- on a host: `ship install`, `update`, `rollback`, `status`, `logs`, `run` and
  `uninstall` manage the services, with checksum verification, a health check
  after every restart and automatic rollback of a failed update.

Each project keeps its own build and its own systemd unit; `ship` only moves the
result around and fills in paths. Design: [docs/superpowers/specs/2026-10-24-ship-design.md](docs/superpowers/specs/2026-10-24-ship-design.md).

## Install

Laptop (macOS or Linux), publish-side commands:

```sh
brew install dmtrkzntsv/tap/ship
```

Server (Linux, root), no credentials needed; downloads the binary from the GitHub
release, verifies it against `SHA256SUMS`, installs completions and creates
`/etc/ship/config.toml` from the example if missing:

```sh
curl -fsSL https://github.com/dmtrkzntsv/ship/releases/latest/download/install.sh | sudo sh
```

Update later with `ship self-update`. On macOS only `publish`, `prune`,
`versions` and `completion` work; host commands exit with `Linux only`.

## Configuration

Copy [config.example.toml](config.example.toml) to `/etc/ship/config.toml` (mode
0600, root) or, on a workstation, `~/.config/ship/config.toml`:

```toml
[r2]
endpoint = "https://<account_id>.r2.cloudflarestorage.com"
access_key_id = "…"
secret_access_key = "…"
bucket = "ship"

[cloudflare]
api_token = "…"     # Zone > Cache Purge on every zone you purge
```

Environment variables override the file: `SHIP_R2_ENDPOINT`,
`SHIP_R2_ACCESS_KEY_ID`, `SHIP_R2_SECRET_ACCESS_KEY`, `SHIP_R2_BUCKET`,
`SHIP_CF_API_TOKEN`.

Use two different R2 tokens:

- **hosts**: Object Read only. Hosts never write to R2, and every host command
  works with a read-only key. Keep it in `/etc/ship/config.toml`.
- **publishing** (CI or laptop): Object Read & Write. Keep it in
  `~/.config/ship/config.toml` or in `SHIP_R2_*` variables, never on a host.

## Publishing

Build your project, put the finished files in a folder, and publish it:

```
<project>-linux-amd64   <project>-linux-arm64   [<project>-linux-arm]
<project>.service       <project>.env.example   (unit and env example optional)
```

`ship publish` writes `release.json` and `SHA256SUMS`, uploads to
`<project>/<version>/`, then replaces the moving channels given in `--channels`
and prunes old versions (default: keep 10; versions a channel points to and the one
just published are always kept). Versions are immutable: an existing version is
refused, except a folder left without `SHA256SUMS` by a failed publish (replaced)
and the identical build (skipped, so a retry still updates the channels).

`--version` is optional: `ship publish econumo --path DIR --channels dev` updates
only the `dev` channel and creates no version folder, which suits builds of every
commit. At least one of `--version` and `--channels` is required.

A moving channel can have any name: a lowercase letter, then lowercase letters,
digits, `-` and `_` (`latest`, `dev`, `edge`, `beta-2`; never `1.2.3`). Its
`release.json` carries `"channel": "<name>"`, which is how ship tells a channel
from a version without any host configuration: hosts installed or updated with
`--version edge` follow every new build of `edge`, and prune never deletes a
channel. `latest` and `dev` are channels by name even in buckets published
before this field existed. A name already published as a version cannot become
a channel, and a channel's name cannot be published as a version; pick another
name.

From a Makefile, so local and CI publishes are identical:

```make
cdn-upload:
	ship publish econumo --path release-out --version $(VERSION) --channels $(CHANNELS)
```

From a GitHub workflow:

```yaml
- uses: dmtrkzntsv/ship@v0        # installs ship onto the PATH, takes no credentials
- run: make cdn-upload VERSION="$VERSION" CHANNELS=latest
  env:
    VERSION: ${{ needs.tag.outputs.version }}
    SHIP_R2_ENDPOINT: ${{ secrets.R2_ENDPOINT }}
    SHIP_R2_ACCESS_KEY_ID: ${{ secrets.R2_ACCESS_KEY_ID }}
    SHIP_R2_SECRET_ACCESS_KEY: ${{ secrets.R2_SECRET_ACCESS_KEY }}
```

The bucket defaults to `ship`; set `SHIP_R2_BUCKET` for another one.

The action's optional `version` input picks another release (`v0.1.0`, `v0`,
`latest`); it outputs the installed `version`. Without it, the action installs
the release it is used at: `@v0` the newest v0.x.y, `@v0.1.0` that one. Pinned
to a commit SHA (`dmtrkzntsv/ship@<sha> # v0.1.0`), it installs the release
tagged at that commit and fails if there is none, so the binary stays pinned
with the action.

## Unit templates

Units are shipped by each project and templated at install time. Placeholders
are filled in anywhere in the file, comments included. Any other `{{…}}` is an
error, except on a comment line (`#` or `;`), where it is left as written. The
result is checked with `systemd-analyze verify`.

| placeholder | value |
|---|---|
| `{{SERVICE}}` | service name `S` |
| `{{USER}}` | `S` (user and group) |
| `{{BIN}}` | `/opt/S/<asset>` |
| `{{BIN_DIR}}` | `/opt/S` |
| `{{ENV_FILE}}` | `/etc/S/<name>.env` |
| `{{DATA_DIR}}` | `/var/lib/S` |
| `{{RUN_DIR}}` | `/run/S` |
| `{{UNIT}}` | unit name of this project ref |

ship creates the user, `/opt/S`, `/etc/S` (`root:S`, 0750), `/var/lib/S`
(owned by `S`) and `/run/S` (through tmpfiles.d), so a unit must not use
`DynamicUser=`, `StateDirectory=` or `RuntimeDirectory=` for them. Files the
service reads itself, such as keys or certificates, can go in `/etc/S`.

### Env example

`<asset>.env.example` is copied **verbatim** to `/etc/S/<name>.env` when no env
file exists yet, and that file is then the live configuration. So:

- it must not hold development defaults or secrets: leave required values empty,
  so a fresh install refuses to start until it is configured;
- placeholders are not filled in, so it cannot say `{{DATA_DIR}}`. `ship run`
  loads only the env file (not the unit's `Environment=` lines) and runs in the
  data dir; for paths that both the service and `ship run` need, set
  `WorkingDirectory={{DATA_DIR}}` in the unit and use paths relative to it
  (`DATABASE_URL=sqlite://app.db`).

## Commands

```
ship install   <ref> [--service S] --version V [--cf-zone Z] [--auto-update]
ship update    <ref> [--service S] [--version V] [--purge-cache] [--auto-update[=false]]
ship update    --auto
ship rollback  <ref> [--service S]
ship uninstall <ref> [--service S] [--purge] [--purge-cache]
ship start|stop|restart <ref> [--service S]
ship logs      <ref> [--service S] [-n N] [--no-follow]
ship run       <ref> [--service S] -- <args...>
ship status    [--service S]
ship versions  <ref>
ship publish   <project> --path DIR [--version V] [--channels C] [--keep N]
ship prune     <project> [--keep N] [--older-than D] [--yes]
ship self-update [--version V]
ship notify test
ship completion bash|zsh|fish
```

A `<ref>` is `<project>` or `<project>/<component>`. Host-changing commands need
root. Run `ship <command> --help` for details; tab completion suggests refs,
services and versions.

`uninstall` keeps what an operator wrote: the ref's env file, and for the
service's last ref `/etc/S`, `/var/lib/S` and the user. `--purge` deletes the
env file, and with the last ref `/etc/S`, `/var/lib/S` and the user (if ship
created it).

`install` never takes over what it did not create: for a service ship does not
manage yet it refuses if `/opt/S` exists, `/etc/S` exists and its group is not
`S`, or `/var/lib/S` exists and is not owned by user `S`. What an earlier
`uninstall` kept is picked up again. System
names such as `root`, `ssh` or `etc` cannot be services. `uninstall --purge`
deletes the service user only if ship created it. An interrupted `update` or
`rollback` (Ctrl-C, SIGTERM, SIGHUP) restores the previous version before exiting.

## Automatic updates

Off by default. A host can follow a moving channel (`latest`, `dev`, `edge`, ...)
on its own: a ref installed with auto-update on is updated within minutes of
each publish, through the same path as `ship update` (checksum verification,
health check, rollback of a failed update).

```sh
sudo ship install econumo --version dev --auto-update
sudo ship update econumo --auto-update          # turn it on for an installed ref
sudo ship update econumo --auto-update=false    # turn it off
```

It is refused on a ref pinned to a version, which never changes. `ship status`
shows the setting in the `AUTO` column: `on`, `-` (off) or `stopped`.

Turning it on writes and enables `ship-auto-update.timer`, which runs
`ship-auto-update.service` (`ship update --auto`) 2 minutes after boot and then
every 5 minutes, with up to a minute of random delay; a run may take up to 30
minutes. A run walks every ref that has auto-update on, and prints nothing for
refs that are up to date. A ref whose unit is not running (installed and not
started yet, or stopped with `ship stop`) is skipped, so an update never starts
a service; it is picked up again once it runs. If systemd cannot be asked
whether the unit runs, the ref counts as "can't check" (see below). The units
stay when the last ref turns auto-update off. They are named
`ship-auto-update.service` and `.timer`; ship refuses to overwrite files of
those names that are not its own, and never gives a service that name. To change the interval:

```sh
sudo systemctl edit ship-auto-update.timer
# [Timer]
# OnUnitActiveSec=
# OnUnitActiveSec=30min
```

Run output is in the journal: `journalctl -u ship-auto-update`. The run exits
non-zero when a ref failed, so `systemctl status ship-auto-update` shows it.

The timer's runs read their settings only from `/etc/ship/config.toml`: R2
credentials or a notification URL exported in your shell (`SHIP_R2_*`,
`SHIP_NOTIFY_URL`, ...) do not reach them. Turning auto-update on warns about
each `SHIP_*` variable set in the shell; put those settings in the file.

While a run holds the state lock, `ship install`, `update`, `rollback`,
`uninstall`, `start`, `stop`, `restart` and `self-update` wait for it to finish
(up to 2 minutes, printing `waiting for another ship run to finish…`; Ctrl-C
stops the wait) instead of failing. They hold the lock while they work, so a run
never restarts a unit you are stopping.

A failed automatic update (the health check fails, or the build is broken) stops
auto-update on that ref, after the previous version is restored: `AUTO` shows
`stopped` and nothing is updated on that ref until you look and run
`ship update <ref> --auto-update` again. A manual `ship rollback` of a ref with
auto-update on stops it the same way, so the next run does not undo it. A manual
`ship update` without the flag updates once and leaves the setting as it is.

### Notifications

Automatic updates notify, and so does every change of the auto-update
setting; other manual commands never do. Set one URL in
`/etc/ship/config.toml` or in `SHIP_NOTIFY_URL`, in
[shoutrrr](https://containrrr.dev/shoutrrr/) format (`telegram://`, `ntfy://`,
`slack://`, `discord://`, `generic://`, `smtp://`); unset means no notifications:

```toml
[notify]
url = "telegram://<bot_token>@telegram?chats=<chat_id>"
```

Check it with `sudo ship notify test`, which sends `homelab: test notification
from ship` the way automatic runs do and prints the error if it fails. Run it
as root: the timer reads only `/etc/ship/config.toml`, and without root the
test uses your own settings instead (it says so).

Every message starts with the host name. There are seven kinds:

- auto-update on/off: `homelab: auto-update on for econumo-beta (dev)` (or
  `off`), sent by `install --auto-update` and by `update --auto-update[=false]`
  when the setting changes, and `homelab: auto-update stopped for econumo-beta
  (dev) after a manual rollback`. The command also prints `<unit>: auto-update
  on|off`. The change is announced once it is saved, even if starting the
  timer then fails. The setting is saved even when the send fails; the command
  then exits non-zero with `notification not sent: <error>`;

- updated: `homelab: econumo (dev) 2026-11-15T10:02 → 2026-11-16T09:40`;
- failed: `homelab: econumo (dev) build X failed, still on Y; auto-update stopped`,
  with the error and the journal lines the health check collected. A checksum
  mismatch is only treated as a failed build when it repeats on the next run
  against the same `SHA256SUMS`; until then it counts as "can't check", since
  a run can overlap a publish;
- down: `homelab: econumo is DOWN — update to X failed and rollback to Y failed;
  auto-update stopped`, with the journal lines;
- not saved: `homelab: econumo (dev) updated to X but the state could not be
  saved; automatic updates halted`, when the update worked but `state.json`
  could not be written. The run stops there and remembers the update as
  pending in `/var/lib/ship/auto-update.json`. The next run, or a manual
  `ship update` of that ref, finds X installed as remembered and only records
  it (no restart, rollback target kept). While the state still cannot be
  saved, later runs update nothing: every run first checks that `state.json`
  can be written, and otherwise counts as "can't run" (below). Without such a
  record, an update always installs, restarts and health-checks;
- can't check: `homelab: can't check econumo (dev) for updates since 14:02: <error>`,
  sent when R2 or the network has been failing for an hour, repeated every 24
  hours (`still can't check ...`), and followed by `... reachable again, was
  failing since 14:02 (3h)` when it recovers. Auto-update stays on;
- can't run: `homelab: automatic updates can't run since 14:02: <error>`, when the
  state file, the R2 settings or the state lock stop a whole run, with the same
  one-hour, 24-hour and recovery rules.

A send is tried up to 3 times within about 30 seconds; if it still fails, the
failure goes to the journal and the run exits non-zero, and the update stays.
An invalid URL makes `ship update --auto` fail at start, and turning auto-update
on is refused while it is invalid.

## Moving a hand-installed service to ship

ship never takes over files it did not create, so a service installed by hand or
by an older script moves over once, with a minute of downtime. With `S` the
service name ship will use and `<project>` the ref:

```sh
# 1. stop the old service and move its files aside (kept for going back)
sudo systemctl disable --now <old-unit>
sudo mv /etc/systemd/system/<old-unit>.service /root/<old-unit>.service.pre-ship
sudo systemctl daemon-reload
sudo mv /etc/S /etc/S.pre-ship 2>/dev/null; sudo mv /opt/S /opt/S.pre-ship 2>/dev/null

# 2. install with ship; it seeds /etc/S/<project>.env from the env example
sudo ship install <project> --service S --version latest

# 3. put the old configuration and any key files back
sudo sh -c 'cat /etc/S.pre-ship/<old env file> > /etc/S/<project>.env'
sudo cp -p /etc/S.pre-ship/<key files> /etc/S/ && sudo chgrp S /etc/S/<key files>

# 4. start and check, then remove the old binary, files and (if renamed) user
sudo ship start <project> --service S
sudo ship logs <project> --service S -n 50 --no-follow
```

The data dir `/var/lib/S` is reused as it is when it is owned by user `S`; when
the old service ran under another user or kept its data elsewhere, move the data
there and `chown -R S:S /var/lib/S` after step 2. Going back: `ship uninstall
<project> --service S` (without `--purge`, which would delete the data dir),
`rm -r /etc/S`, move the `.pre-ship` files back, and re-enable the old unit.

## Development

```sh
make lint test      # go vet, gofmt check, unit tests (no network needed)
make dist VERSION=v0.0.0
```

Releases are cut with the `release` workflow (manual dispatch).
