# ship — one tool to publish and run all lab services

Status: approved · 2026-10-29

## Why

`econumo-deploy.sh` deploys Econumo and Billing well: it pulls a channel from
R2, verifies checksums, installs a hardened systemd unit, rolls back a failed
update, and can uninstall, run management commands and purge the Cloudflare
cache. But it only knows two projects, generates one generic unit, and every
project repo carries its own copy of the R2 upload logic (`make cdn-upload` in
econumo, billing and winterflow; twillingate does not publish to R2 at all).

The goal is to deploy every service-based project — econumo, billing,
twillingate (+ twillingate-sql), winterflow (standalone and distributed) — the
same way, with:

- one **CLI tool**, `ship`, written in Go, with its own config and state
  and shell completion. On a laptop or in CI it publishes a project's built
  files to the right R2 folder (`ship publish`, called from each project's
  Makefile); on a host it installs and operates them;
- a **setup Action** in this repo that installs `ship` in a workflow, and a
  **Homebrew formula** in `dmtrkzntsv/homebrew-tap`.

## Goals

- Any project publishes with one `ship publish` call — from its Makefile,
  locally or in CI — after its own build. In CI, `uses: dmtrkzntsv/ship@v1`
  only puts `ship` on the PATH.
- Any host installs, updates, rolls back, operates and removes any project with
  the same commands.
- Each project repo owns how it runs: its systemd unit ships with its release.
- Several services of the same project coexist on one host (econumo,
  econumo-beta, econumo-test).
- Several projects can share one service — one user, one data dir, one runtime
  dir — when they work together (twillingate + twillingate-sql over a Unix
  socket).
- Tab completion for commands, projects, services and versions.

## Non-goals

- Building projects. Builds differ too much (cgo/DuckDB on native runners, SPA
  embedding, four winterflow binaries); each project keeps its build and only
  hands `ship publish` a folder of finished files.
- Docker images, static sites, one-off jobs.
- Managing dependencies a service needs (Postgres, Redis, Docker, Litestream,
  reverse proxy). Units may reference them; the tool does not install them.
- Orchestrating several hosts at once. `ship` acts on the host it runs on.
- Changing the project repos. Moving econumo, billing, twillingate,
  twillingate-sql and winterflow onto `ship publish` and templated units is
  follow-up work in each repo (listed at the end).

## Anti-goals (would be a failure even if it works)

- A failed update that leaves a service down or on a half-swapped
  binary/unit pair.
- Secrets, hostnames, domains or zone ids in this repo — it is public.
- A publish that silently overwrites a released version.
- The tool needing per-project knowledge in this repo to deploy a new project.
- A default that moves a beta/test service onto `latest` by accident.

## Concepts

- **Project** — something published to R2: `econumo`, `billing`,
  `twillingate`, `twillingate-sql`, `winterflow`. A project may contain several
  **components**, addressed as `<project>/<component>` (`winterflow/hub`).
  A *project ref* is either `<project>` or `<project>/<component>`.
- **Name** of a project ref — the component if there is one, else the project
  (`econumo` → `econumo`, `winterflow/hub` → `hub`). Used for file names on
  the host.
- **Channel** — a folder under the project in R2. `latest` and `dev` are
  *moving* channels; any other name (`v1.2.3`, `v261024.3`, `2026.10.24-1`) is
  an *immutable* version channel.
- **Service** — a named deployment on one host: one system user and group of
  the same name, one data dir, one runtime dir, one config dir. It holds one or
  more project refs. Defaults to the project name (`winterflow/hub` →
  `winterflow`).

## Repository bucket layout

Bucket is configurable, default `lab` (it is being recreated as a generic
bucket; nothing depends on the name).

```
s3://<bucket>/<project>/<channel>/
    <asset>-linux-amd64          one per architecture published
    <asset>-linux-arm64
    <asset>-linux-arm            optional (winterflow builds 32-bit ARM)
    <asset>.service              unit template, one per component
    <asset>.env.example          optional; seeds the env file on first install
    release.json                 {project, version, commit, published_at}
                                 (version is the channel name for a
                                 channel-only publish; commit from
                                 `git rev-parse HEAD` when run in a repo)
    SHA256SUMS                   covers every other file in the folder
```

`<asset>` is `<project>` for a plain project and `<project>-<component>` for a
component (`winterflow-hub-linux-amd64`, `winterflow-hub.service`). This
matches the names econumo, billing and winterflow already produce.

`release.json` exists so that a host on a moving channel can report the real
version it runs (`latest` → `v1.4.2`).

## Publishing

### `ship publish`

Every project publishes through the same command, called from its own
Makefile so a local `make publish-dev` and CI do exactly the same thing:

```make
cdn-upload:
	ship publish econumo --path release-out --version $(VERSION) --channels $(CHANNELS)
```

`ship publish <project> --path DIR [--version V] [--channels latest,dev] [--keep N] [--bucket B]`

- `--path` — folder with the built files (binaries, `*.service`, optional
  `*.env.example`).
- `--version` — the release version, written to `release.json` and used as
  the immutable channel. Optional: without it only the `--channels` are
  updated and no version folder is created — econumo's dev publishes work
  this way today. At least one of `--version` / `--channels` is required.
- `--channels` — moving channels to also update (`latest`, `dev`).
- `--keep` — version channels to keep after publishing, default `10`; `0`
  disables pruning.

Behaviour:

- Validates the folder before uploading anything: file names match the
  layout; every asset has a binary for at least amd64 and arm64; units
  contain only known placeholders. A binary without a unit is allowed (a
  *tool* project, like `ship` itself); `ship install` refuses such a
  project.
- Writes `release.json` and `SHA256SUMS` itself (projects no longer do).
- Uploads to `<project>/<version>/` first; refuses if that folder already
  exists (immutable). Then copies to each moving channel, replacing it.
- Cache-Control: immutable channels `public, max-age=31536000, immutable`;
  moving channels `no-cache`. Content-Type `application/octet-stream`.
- Sends no request checksums R2 rejects (the reason the script sets
  `AWS_REQUEST_CHECKSUM_CALCULATION=when_required`).
- A moving channel is replaced so that `SHA256SUMS` is uploaded **last**, and
  the installer always verifies against it, so a host racing an upload fails
  the checksum instead of installing a mixed set.
- `version` must match `^[A-Za-z0-9._-]+$` and must not be `latest`/`dev`.
- After a fully successful publish it prunes the project to `--keep`
  versions (owner's decision: automatic, 10 by default). A prune failure
  exits non-zero but never undoes the publish.
- Needs write credentials (see *Credentials*).

### The setup Action

Composite action at the repo root (`action.yml`), used as
`uses: dmtrkzntsv/ship@v1`. It only installs the released `ship` binary
matching the action's ref onto the PATH (download from the GitHub release,
verify against its `SHA256SUMS`) — like `actions/setup-go`. Optional input
`version` overrides which release is installed. It takes no credentials; the
workflow passes `SHIP_R2_*` from secrets to the `make` step that calls
`ship publish`:

```yaml
- uses: dmtrkzntsv/ship@v1
- run: make cdn-upload VERSION=${{ needs.tag.outputs.version }} CHANNELS=latest
  env:
    SHIP_R2_ENDPOINT: ${{ secrets.R2_ENDPOINT }}
    SHIP_R2_ACCESS_KEY_ID: ${{ secrets.R2_ACCESS_KEY_ID }}
    SHIP_R2_SECRET_ACCESS_KEY: ${{ secrets.R2_SECRET_ACCESS_KEY }}
```

The repo's own release workflow tags `vX.Y.Z` and moves the major tag (`v1`)
so `@v1` keeps working.

### Pruning old versions

`ship prune <project> [--keep N] [--older-than DURATION] [--yes]`

- Version channels are ordered by `published_at` from their `release.json`
  (a folder without one is ordered by its newest object's LastModified and
  reported as such).
- Keeps the newest `N` (default 10); with `--older-than` deletes only versions
  that are also older than that.
- Never deletes `latest`, `dev`, or any version a moving channel's
  `release.json` currently points to (those do not count toward `N`).
- Without `--yes` it only prints what it would delete and the bytes freed.
- Needs write credentials, taken from `SHIP_R2_*` environment variables
  (as in CI or on a workstation); a host's read-only key fails with a clear
  message.
- Cannot know what hosts run: a pruned version can no longer be selected with
  `--version`. Running services and `rollback` (local `.prev`) are
  unaffected.
- Prunes the whole project, components included (they share a version
  folder).

## The CLI

One static Go binary (`CGO_ENABLED=0`), built for linux amd64/arm64/arm and
darwin amd64/arm64; on hosts installed at `/usr/local/bin/ship`. On macOS
only `publish`, `prune`, `versions` and `completion` work; host commands exit
with "Linux only". Talks to R2 (S3 API) and Cloudflare natively; no aws
CLI or curl. Shells out only to `systemctl`, `journalctl`, `systemd-run`,
`systemd-analyze`, `useradd`, `userdel`. Uses cobra for commands, help and
completion. Every command that changes the host requires root.

### Commands

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
ship versions  <ref>                     # channels and versions in R2
ship publish   <project> --path DIR [--version V] [--channels C] [--keep N]
ship prune     <project> [--keep N] [--older-than D] [--yes]
ship self-update [--version V]
ship completion bash|zsh|fish
```

Examples:

```
ship install   econumo --service econumo-beta --version dev
ship update    econumo --service econumo-beta              # keeps following dev
ship install   twillingate     --version latest
ship install   twillingate-sql --service twillingate --version latest
ship install   winterflow/hub  --service wf --version latest
ship run       econumo --service econumo-beta -- currency:update-rates
```

### Paths on the host

For service `S` and a project ref whose name is `N` and asset is `A`:

| what | path |
|---|---|
| binary | `/opt/S/A` (previous: `/opt/S/A.prev`) |
| env file | `/etc/S/N.env` (mode 0640, root:S) |
| data dir | `/var/lib/S` (0750, S:S) — shared by the service's projects |
| runtime dir | `/run/S` (0750, S:S) — shared; created by the tool via a `tmpfiles.d` entry so it survives reboots and is not removed when one unit stops |
| unit | `/etc/systemd/system/<unit>.service`, see *Unit names* |
| system user | `S` (no login, home `/var/lib/S`) |

### Unit names

Chosen once, at install (or adopt), and stored in state; never recomputed:

1. The first ref installed into a service gets `S.service`.
2. A later ref whose name `N` already starts with `S-` gets `N.service`.
3. Any other later ref gets `S-N.service`.

Uninstalling a ref never renames the others. Examples: `econumo-beta.service`,
`econumo-billing.service` (billing installed as service `econumo-billing`),
`twillingate.service` + `twillingate-sql.service`, `wf.service` (hub, first)
+ `wf-agent.service`. Existing script-installed hosts keep their unit names.

### Unit templates

Each project ships its own unit. The tool replaces these placeholders and
rejects any other `{{…}}`:

| placeholder | value |
|---|---|
| `{{SERVICE}}` | `S` |
| `{{USER}}` | `S` (user and group) |
| `{{BIN}}` | `/opt/S/A` |
| `{{BIN_DIR}}` | `/opt/S` |
| `{{ENV_FILE}}` | `/etc/S/N.env` |
| `{{DATA_DIR}}` | `/var/lib/S` |
| `{{RUN_DIR}}` | `/run/S` |
| `{{UNIT}}` | the unit name of this project ref |

After substitution the unit is checked with `systemd-analyze verify` before it
is installed. Example (twillingate-sql):

```
[Service]
User={{USER}}
Group={{USER}}
EnvironmentFile={{ENV_FILE}}
Environment=TWSQL_SOCKET={{RUN_DIR}}/sql.sock
ExecStart={{BIN}}
ReadOnlyPaths={{DATA_DIR}}
ReadWritePaths={{RUN_DIR}}
...
```

### Lifecycle

- **install** — downloads binary (host arch), unit, optional env example,
  `release.json` and `SHA256SUMS`; verifies every file before touching disk;
  creates the user, dirs and tmpfiles entry if missing; installs binary and
  unit; creates the env file from the example (or empty) only if none exists;
  `daemon-reload`, `enable`; **does not start** (same as today — the env needs
  configuring first). Records the ref, channel, version and `--cf-zone` in
  state. Refuses if the ref is already installed in that service.
- **update** — channel is `--version` if given (and then remembered), else the
  remembered one. Same download/verify. Backs up binary and unit as `.prev`,
  swaps both, `daemon-reload`, restarts, then watches the unit for 10 s
  (`systemctl is-active`, as today). If it fails: restores both, restarts,
  prints the last 30 log lines, exits non-zero. Prints old → new version.
  Restarts only this ref's unit, never its siblings.
- **rollback** — swaps `.prev` back for binary and unit, restarts, checks
  health; state records the rolled-back version.
- **uninstall** — stops, disables and removes this ref's unit, binary and env
  file. When it is the service's last ref, also removes `/opt/S`, `/etc/S`, the
  tmpfiles entry and — only with `--purge` — `/var/lib/S`; the user is removed
  only if the data dir is gone (same reasoning as today). Tolerates
  half-installed services.
- **start / stop / restart** — as today; start and restart wait for health.
- **logs** — `journalctl -u <unit> -f`; if the unit sets `LogNamespace=`
  (winterflow does) it adds `--namespace`.
- **run** — `systemd-run --pty --wait --collect` with the ref's env file,
  user, working dir and `ReadWritePaths` of the data dir, as today.
- **status** — table of services, refs, channel, installed version, unit state.
- `--purge-cache` purges the service's Cloudflare zone (`purge_everything`)
  as the very last step of a successful update or uninstall, never after a
  rollback. Checks the API body for `"success":true`. Fails up front if the
  service has no zone or no token is configured.

Runs are serialised with a lock on the state file.

### Config and state

- `/etc/ship/config.toml` — edited by hand, mode 0600, root:

  ```toml
  [r2]
  endpoint = "https://<account_id>.r2.cloudflarestorage.com"
  access_key_id = "…"
  secret_access_key = "…"
  bucket = "lab"

  [cloudflare]
  api_token = "…"     # Zone > Cache Purge on every zone you purge
  ```

  Environment variables override: `SHIP_R2_ENDPOINT`,
  `SHIP_R2_ACCESS_KEY_ID`, `SHIP_R2_SECRET_ACCESS_KEY`,
  `SHIP_R2_BUCKET`, `SHIP_CF_API_TOKEN`. The repo ships
  `config.example.toml` with placeholders only.

- **Credentials** (owner's decision):
  - Hosts: one R2 API token, *Object Read* on the bucket only, shared by all
    projects. `ship` never writes to R2 from a host (`publish` runs in CI).
  - Publishing (CI or laptop): one separate R2 token, *Object Read & Write*
    on the bucket. On a laptop it lives in `~/.config/ship/config.toml`
    (same format, mode 0600) or `SHIP_R2_*` variables — never in the host's
    `/etc/ship/config.toml`. In CI it is stored as
    org secrets in `econumo` / `winterflow-org` and as repo secrets in
    personal-account repos. R2 scopes tokens per bucket, not per prefix, so
    per-project keys would add no isolation.
  - Cloudflare: one token, *Zone → Cache Purge* only, for the deployed zones;
    the zone id is per service in state.
  - Host commands must work with a read-only key (`ListObjectsV2` and
    `GetObject` only).
  - Config lookup order: environment variables, then
    `~/.config/ship/config.toml` when not root, then
    `/etc/ship/config.toml`.

- `/var/lib/ship/state.json` — written only by the tool, mode 0644 (no
  secrets, so completion works without sudo). Per service: Cloudflare zone id;
  per ref: channel, installed version, previous version, installed-at.

- `/var/cache/ship/` — cached R2 listings (projects, channels) written by
  any root run, readable by all, used by completion; refreshed when older than
  a few minutes and credentials are readable.

### Completion

`ship completion bash|zsh|fish` (cobra). `install.sh` installs it for the
shells present. Dynamic suggestions: project refs from the R2 cache;
`--service` from state (and, for `install`, the project name); `--version`
from the ref's channels in the cache; for commands other than `install`, refs
already installed in the chosen service.

### Shipping the tool itself

- This repo is **public** (the owner flips visibility). A private repo under a
  personal account cannot provide an Action to repos in the `econumo` org.
- Release workflow (manual dispatch): tags `vX.Y.Z`, moves `v1`, builds
  linux amd64/arm64/arm and darwin amd64/arm64, creates a GitHub release with
  the raw binaries (`ship-<os>-<arch>`, for `install.sh`, the setup Action
  and `self-update`), tarballs `ship-<version>-<os>-<arch>.tar.gz` (for
  Homebrew), `SHA256SUMS` and `install.sh`; publishes the linux binaries to
  `s3://<bucket>/ship/<version>/` + `latest` with `ship publish`; then
  updates the Homebrew formula.
- **Homebrew**: `brew install dmtrkzntsv/tap/ship`. The release workflow
  writes `Formula/ship.rb` in `dmtrkzntsv/homebrew-tap` and pushes it, the
  same way gosaid's release does (`scripts/update-brew-formula.sh`, token in
  a `TAP_GITHUB_TOKEN` secret). The formula points at the release tarballs
  per OS/arch (`on_macos`/`on_linux`, `on_arm`/`on_intel`), installs the
  binary, and installs completions with
  `generate_completions_from_executable(bin/"ship", "completion")`. It is
  meant for laptops; servers use `install.sh` (`/usr/local/bin`, root).
- First install on a fresh host, no credentials needed:
  `curl -fsSL https://github.com/dmtrkzntsv/ship/releases/latest/download/install.sh | sudo sh`
  — downloads the binary for the arch from the GitHub release, verifies the
  checksum, installs to `/usr/local/bin`, installs completion, creates
  `/etc/ship/config.toml` from the example if missing.
- `ship self-update [--version V]` reads `ship/<channel>` from R2
  (default `latest`; a given `--version` is remembered in state), verifies,
  replaces itself atomically.

## Migrating existing hosts

> **Superseded (2026-10-31):** `ship adopt` was removed in favour of one
> manual move per service, the same for every project (README, "Moving a
> hand-installed service to ship"). The text below records the original design.

Services installed by `econumo-deploy.sh` (unit at
`/etc/systemd/system/S.service`, binary `/opt/S/<project>`, env
`/etc/S/env`, data `/var/lib/S`) are taken over with an explicit
`ship adopt <project> --service S --version V`:

- refuses unless that exact layout is present and `S` is not yet in state;
- refuses clearly if the channel has no unit template (so a project is
  adopted only after its repo publishes one — no built-in fallback unit);
- keeps the user, data dir and unit name (`S.service`);
- moves `/etc/S/env` to `/etc/S/<project>.env`;
- installs the channel's binary and templated unit exactly like an update
  (old binary and unit kept as `.prev`), restarts, health-checks and rolls
  everything back — including the env file move — on failure;
- records the service, ref, unit name, channel and version in state.

`install` on a host that already has the old layout refuses and points at
`adopt`.

`econumo-deploy.sh` is removed from the repo once the Go tool covers it
(history keeps it).

## Decided by the owner

- Builds stay in each project; each Makefile publishes with `ship publish`; the Action only installs `ship`.
- `ship` is published to the `dmtrkzntsv/homebrew-tap` tap.
- Each project repo owns its systemd unit; the tool templates paths/users into
  it so several services of one project coexist.
- Services can hold several projects sharing user, data and runtime dir
  (twillingate + twillingate-sql; winterflow api/hub/agent).
- CLI shape: project first, `--service`, `--version`.
- Unit names: first ref gets `S.service`, later ones `S-N.service` (or `N.service` when `N` starts with `S-`), fixed at install.
- Go rewrite, installed as a tool, its own config and remembered versions,
  shell completion.
- Generic bucket, `s3://lab/<project>/<channel>/`.
- Twillingate also publishes plain binaries to R2 (its public GitHub release
  with tarballs stays).
- This repo goes public.
- Old versions are pruned automatically after every publish, keeping 10.

## Calls made in this document (owner to check)

- Subcommands (`ship install …`) instead of mode flags, for completion.
- `--service` defaults to the project name; `--version` remembered per ref.
- One env file per ref, `/etc/S/<name>.env`.
- Runtime dir created by the tool, not `RuntimeDirectory=`.
- State in `/var/lib/ship/state.json` (not `/etc/ship/`), so the
  hand-edited config is never rewritten.
- Cloudflare zone stored per service; one API token in config.
- No `aws` profile support; keys from config/env only.
- The Action installs a released binary rather than building from source.
- macOS builds; host commands are Linux-only.
- Laptop write credentials in `~/.config/ship/config.toml`.
- `release.json` added to every published channel.
- Only `latest` and `dev` are moving channels.
- Health check stays "unit stays active for 10 s".
- (Removed 2026-10-31, see above.) Adoption is a separate `ship adopt` command that restarts with
  health check and rollback; no fallback unit.
- Tool projects (binary, no unit) are publishable but not installable;
  `self-update` defaults to `latest`.

## Left to the builder

- Go package layout, S3 client library, TOML library, test approach (an
  end-to-end test against a systemd container or VM is expected before v1).
- Exact output formatting, `status` table, cache refresh interval.
- How `install.sh` detects shells for completion.

## Follow-up in other repos (not this work)

- econumo, billing: make `cdn-upload` call `ship publish`, drop their
  `SHA256SUMS` steps, add the setup Action to workflows;
  publish `deployment/systemd/<project>.service` as a template.
- twillingate: add R2 publishing of plain binaries + template unit; keep the
  GitHub release tarballs.
- twillingate-sql: release workflow + template unit using `{{RUN_DIR}}` for
  the socket and read-only `{{DATA_DIR}}`.
- winterflow: template units for standalone, api, hub, agent;
  `cdn-upload` via `ship publish`; set R2 secrets for the new bucket.
- homebrew-tap: add a fine-grained token with write access to the tap as
  `TAP_GITHUB_TOKEN` in this repo.
- Move all secrets to the new `lab` bucket credentials.
