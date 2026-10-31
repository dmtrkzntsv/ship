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
bucket = "lab"

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
`<project>/<version>/`, then replaces the moving channels (`latest`, `dev`) and
prunes old versions (default: keep 10; versions a channel points to and the one
just published are always kept). Versions are immutable: an existing version is
refused, except a folder left without `SHA256SUMS` by a failed publish (replaced)
and the identical build (skipped, so a retry still updates the channels).

`--version` is optional: `ship publish econumo --path DIR --channels dev` updates
only the `dev` channel and creates no version folder, which suits builds of every
commit. At least one of `--version` and `--channels` is required.

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

The bucket defaults to `lab`; set `SHIP_R2_BUCKET` for another one.

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
ship install   <ref> [--service S] --version V [--cf-zone Z]
ship adopt     <ref> [--service S] --version V [--cf-zone Z]
ship update    <ref> [--service S] [--version V] [--purge-cache]
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

## Migrating a host from the old deploy script

Hosts installed by the former `econumo-deploy.sh` are taken over in place:

```sh
sudo ship adopt econumo --service econumo --version latest
```

`adopt` keeps the user, data directory and unit name, moves the env file to
`/etc/S/<project>.env`, installs the channel's binary and templated unit, restarts
with a health check and rolls everything back on failure. It refuses until the
project publishes a unit template. The script's binary and unit are kept as the
previous pair, but `ship rollback` is unavailable right after `ship adopt` (ship
does not know their version) until the next `ship update`.

## Development

```sh
make lint test      # go vet, gofmt check, unit tests (no network needed)
make dist VERSION=v0.0.0
```

Releases are cut with the `release` workflow (manual dispatch).
