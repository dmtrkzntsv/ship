# deploy — one tool to publish and run all lab services

Status: draft, awaiting owner review · 2026-10-24

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

- one **GitHub Action** in this repo that every project uses to publish its
  built artifacts to the right R2 folder;
- one **installed CLI tool**, `deploy`, written in Go, with its own config and
  state and shell completion, that installs and operates those artifacts on a
  host.

## Goals

- Any project publishes with one `uses: dmtrkzntsv/deploy@v1` step after its
  own build.
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
  hands the Action a folder of finished files.
- Docker images, static sites, one-off jobs.
- Managing dependencies a service needs (Postgres, Redis, Docker, Litestream,
  reverse proxy). Units may reference them; the tool does not install them.
- Orchestrating several hosts at once. `deploy` acts on the host it runs on.
- Changing the project repos. Moving econumo, billing, twillingate,
  twillingate-sql and winterflow onto the Action and templated units is
  follow-up work in each repo (listed at the end).

## Anti-goals (would be a failure even if it works)

- A failed update that leaves a service down or on a half-swapped
  binary/unit pair.
- Secrets, hostnames, domains or zone ids in this repo — it is public.
- An Action that silently overwrites a released version.
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
    SHA256SUMS                   covers every other file in the folder
```

`<asset>` is `<project>` for a plain project and `<project>-<component>` for a
component (`winterflow-hub-linux-amd64`, `winterflow-hub.service`). This
matches the names econumo, billing and winterflow already produce.

`release.json` exists so that a host on a moving channel can report the real
version it runs (`latest` → `v1.4.2`).

## The GitHub Action

Composite action at the repo root (`action.yml`), used as
`dmtrkzntsv/deploy@v1`. It builds the `deploy` binary from the action's own
checkout (`${{ github.action_path }}`, with `actions/setup-go`) and runs
`deploy publish`, so publishing and installing share one implementation of the
layout and checksums, and the code that runs is exactly the pinned ref.

Inputs:

| input | required | meaning |
|---|---|---|
| `project` | yes | R2 project folder |
| `path` | yes | local folder with the built files (binaries, `*.service`, optional `*.env.example`) |
| `version` | yes | the release version, written to `release.json` and used as the immutable channel |
| `channels` | no | extra moving channels to also update, e.g. `latest` or `latest,dev` |
| `endpoint`, `access-key-id`, `secret-access-key` | yes | R2 credentials (from the calling repo's secrets) |
| `bucket` | no | default `lab` |

`deploy publish` behaviour:

- Validates the folder before uploading anything: file names match the
  layout; every asset has a binary for at least amd64 and arm64; units
  contain only known placeholders. A binary without a unit is allowed (a
  *tool* project, like `deploy` itself); `deploy install` refuses such a
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

The repo's own release workflow tags `vX.Y.Z` and moves the major tag (`v1`)
so `@v1` keeps working.

## The CLI

One static Go binary (`CGO_ENABLED=0`), linux amd64/arm64/arm, installed at
`/usr/local/bin/deploy`. Talks to R2 (S3 API) and Cloudflare natively; no aws
CLI or curl. Shells out only to `systemctl`, `journalctl`, `systemd-run`,
`systemd-analyze`, `useradd`, `userdel`. Uses cobra for commands, help and
completion. Every command that changes the host requires root.

### Commands

```
deploy install   <ref> [--service S] --version V [--cf-zone Z]
deploy adopt     <ref> [--service S] --version V [--cf-zone Z]
deploy update    <ref> [--service S] [--version V] [--purge-cache]
deploy rollback  <ref> [--service S]
deploy uninstall <ref> [--service S] [--purge] [--purge-cache]
deploy start|stop|restart <ref> [--service S]
deploy logs      <ref> [--service S] [-n N] [--no-follow]
deploy run       <ref> [--service S] -- <args...>
deploy status    [--service S]
deploy versions  <ref>                     # channels and versions in R2
deploy publish   ...                       # used by the Action
deploy self-update [--version V]
deploy completion bash|zsh|fish
```

Examples:

```
deploy install   econumo --service econumo-beta --version dev
deploy update    econumo --service econumo-beta              # keeps following dev
deploy install   twillingate     --version latest
deploy install   twillingate-sql --service twillingate --version latest
deploy install   winterflow/hub  --service wf --version latest
deploy run       econumo --service econumo-beta -- currency:update-rates
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

- `/etc/deploy/config.toml` — edited by hand, mode 0600, root:

  ```toml
  [r2]
  endpoint = "https://<account_id>.r2.cloudflarestorage.com"
  access_key_id = "…"
  secret_access_key = "…"
  bucket = "lab"

  [cloudflare]
  api_token = "…"     # Zone > Cache Purge on every zone you purge
  ```

  Environment variables override: `DEPLOY_R2_ENDPOINT`,
  `DEPLOY_R2_ACCESS_KEY_ID`, `DEPLOY_R2_SECRET_ACCESS_KEY`,
  `DEPLOY_R2_BUCKET`, `DEPLOY_CF_API_TOKEN`. The repo ships
  `config.example.toml` with placeholders only.

- **Credentials** (owner's decision):
  - Hosts: one R2 API token, *Object Read* on the bucket only, shared by all
    projects. `deploy` never writes to R2 from a host (`publish` runs in CI).
  - CI: one separate R2 token, *Object Read & Write* on the bucket, stored as
    org secrets in `econumo` / `winterflow-org` and as repo secrets in
    personal-account repos. R2 scopes tokens per bucket, not per prefix, so
    per-project keys would add no isolation.
  - Cloudflare: one token, *Zone → Cache Purge* only, for the deployed zones;
    the zone id is per service in state.
  - `deploy` must work with a read-only key: no write, list-only-what-it-
    needs (`ListObjectsV2` on the bucket for completion and `versions`).

- `/var/lib/deploy/state.json` — written only by the tool, mode 0644 (no
  secrets, so completion works without sudo). Per service: Cloudflare zone id;
  per ref: channel, installed version, previous version, installed-at.

- `/var/cache/deploy/` — cached R2 listings (projects, channels) written by
  any root run, readable by all, used by completion; refreshed when older than
  a few minutes and credentials are readable.

### Completion

`deploy completion bash|zsh|fish` (cobra). `install.sh` installs it for the
shells present. Dynamic suggestions: project refs from the R2 cache;
`--service` from state (and, for `install`, the project name); `--version`
from the ref's channels in the cache; for commands other than `install`, refs
already installed in the chosen service.

### Shipping the tool itself

- This repo is **public** (the owner flips visibility). A private repo under a
  personal account cannot provide an Action to repos in the `econumo` org.
- Release workflow (manual dispatch): tags `vX.Y.Z`, moves `v1`, builds
  linux amd64/arm64/arm, creates a GitHub release with the binaries,
  `SHA256SUMS` and `install.sh`, and publishes `deploy` to
  `s3://<bucket>/deploy/<version>/` + `latest` with its own Action.
- First install on a fresh host, no credentials needed:
  `curl -fsSL https://github.com/dmtrkzntsv/deploy/releases/latest/download/install.sh | sudo sh`
  — downloads the binary for the arch from the GitHub release, verifies the
  checksum, installs to `/usr/local/bin`, installs completion, creates
  `/etc/deploy/config.toml` from the example if missing.
- `deploy self-update [--version V]` reads `deploy/<channel>` from R2
  (default `latest`; a given `--version` is remembered in state), verifies,
  replaces itself atomically.

## Migrating existing hosts

Services installed by `econumo-deploy.sh` (unit at
`/etc/systemd/system/S.service`, binary `/opt/S/<project>`, env
`/etc/S/env`, data `/var/lib/S`) are taken over with an explicit
`deploy adopt <project> --service S --version V`:

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

- Upload-only Action; builds stay in each project.
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

## Calls made in this document (owner to check)

- Subcommands (`deploy install …`) instead of mode flags, for completion.
- `--service` defaults to the project name; `--version` remembered per ref.
- One env file per ref, `/etc/S/<name>.env`.
- Runtime dir created by the tool, not `RuntimeDirectory=`.
- State in `/var/lib/deploy/state.json` (not `/etc/deploy/`), so the
  hand-edited config is never rewritten.
- Cloudflare zone stored per service; one API token in config.
- No `aws` profile support; keys from config/env only.
- The Action builds `deploy` from source and runs `deploy publish`.
- `release.json` added to every published channel.
- Only `latest` and `dev` are moving channels.
- Health check stays "unit stays active for 10 s".
- Adoption is a separate `deploy adopt` command that restarts with
  health check and rollback; no fallback unit.
- Tool projects (binary, no unit) are publishable but not installable;
  `self-update` defaults to `latest`.

## Left to the builder

- Go package layout, S3 client library, TOML library, test approach (an
  end-to-end test against a systemd container or VM is expected before v1).
- Exact output formatting, `status` table, cache refresh interval.
- How `install.sh` detects shells for completion.

## Follow-up in other repos (not this work)

- econumo, billing: drop `cdn-upload`/`SHA256SUMS` steps, call the Action;
  publish `deployment/systemd/<project>.service` as a template.
- twillingate: add R2 publishing of plain binaries + template unit; keep the
  GitHub release tarballs.
- twillingate-sql: release workflow + template unit using `{{RUN_DIR}}` for
  the socket and read-only `{{DATA_DIR}}`.
- winterflow: template units for standalone, api, hub, agent; call the
  Action; set R2 secrets for the new bucket.
- Move all secrets to the new `lab` bucket credentials.
