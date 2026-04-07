---
title: "feat: Add Docker support to krng"
type: feat
status: active
date: 2026-04-07
---

# feat: Add Docker support to krng

## Overview

Enable running Docker commands inside a krng sandbox. Currently krng cannot run Docker because it (1) never mounts `/sys` (Docker needs `/sys/fs/cgroup`), (2) uses `--unshare-all` which unshares the cgroup namespace (Docker needs cgroup access to manage containers), and (3) has no guard against user config mounts overwriting built-in `/proc`, `/dev`, and `/run` mounts.

## Problem Frame

A user running `krng bash` and then `docker ps` gets a connection error because the Docker socket is hidden behind `--tmpfs /run`, `/sys` is unmounted, and cgroup namespace isolation prevents container management. The user's workaround — adding `--ro-bind /run /run`, `--ro-bind /dev /dev`, `--ro-bind /proc /proc` via config — makes things worse because those mounts overwrite krng's built-in `--proc`, `--dev`, and `--tmpfs /run` mounts (bwrap processes mounts left-to-right).

## Requirements Trace

- R1. Docker CLI commands (`docker ps`, `docker run`, `docker build`) must work inside krng when Docker support is enabled
- R2. Docker support must be opt-in — default sandbox behavior must not change
- R3. Built-in mounts (`--proc`, `--dev`, `--tmpfs /run`) must be protected from accidental override by user config
- R4. The solution must not weaken the sandbox for non-Docker use cases

## Scope Boundaries

- Docker-in-Docker (running dockerd inside the sandbox) is out of scope — only Docker CLI → host daemon communication
- Podman, containerd CLI, and other container runtimes are not explicitly targeted but may benefit
- No changes to the project config security model (Docker config stays global-only)

## Key Technical Decisions

- **Individual unshare flags instead of `--unshare-all`**: bwrap has no `--share-cgroup` flag (only `--share-net` exists as an override). When Docker mode is enabled, krng must enumerate individual `--unshare-*` flags and omit `--unshare-cgroup`. This is the only way to keep cgroup namespace shared.
- **`/sys` mounted read-only only when Docker is enabled**: Mounting `/sys` exposes hardware info and kernel parameters. While generally safe read-only, keeping it unmounted by default preserves the minimal-surface principle.
- **Mount conflict detection via validation, not silent reordering**: Rather than silently fixing mount order, reject config entries that would overwrite built-in mounts (`/proc`, `/dev`, `/run`, `/sys`) with a clear error message. This teaches users the correct approach.
- **Docker socket path auto-detection**: Resolve the real path of the Docker socket (following symlinks like `/var/run` → `/run`) rather than hardcoding `/var/run/docker.sock`.

## Open Questions

### Resolved During Planning

- **Should Docker support be a single flag or granular options?** → Single `docker = true` flag. Granular cgroup/sys control adds complexity without clear use cases beyond Docker. Can be decomposed later if needed.
- **Should `/sys` always be mounted?** → No, only when `docker = true`. Keeps default sandbox minimal.
- **Where should Docker socket be mounted — `ro_bind` or `bind`?** → `bind` (read-write). Socket connections bypass mount RO flags anyway, so `ro_bind` provides no real protection, but some Docker operations may need write access to the socket path.

### Deferred to Implementation

- Exact error message wording for mount conflict detection
- Whether to detect Docker socket path at build time or runtime (likely runtime via `os.Stat`)

## Implementation Units

- [ ] **Unit 1: Add `docker` config field**

  **Goal:** Add `Docker *bool` field to `Config` and `ProjectConfig`, with TOML parsing, merge logic, and env override (`KRNG_DOCKER`).

  **Requirements:** R2, R4

  **Dependencies:** None

  **Files:**
  - Modify: `config.go`
  - Modify: `examples/config.toml`
  - Test: `sandbox_test.go`

  **Approach:**
  - Add `Docker *bool` field to both `Config` and `ProjectConfig` structs with `toml:"docker"` tag
  - Add merge logic in `mergeConfigs` (same pattern as `NewSession`, `ShareTmp`)
  - Add `KRNG_DOCKER` env override in `applyEnvOverrides` (same pattern as `KRNG_SHARE_TMP`)
  - Add commented example in `examples/config.toml`

  **Patterns to follow:**
  - `ShareTmp` field: same pointer-bool pattern, same merge/env-override approach

  **Test scenarios:**
  - Happy path: `Docker` field parsed from TOML config as true/false
  - Happy path: `mergeConfigs` — project `Docker` overrides global when set
  - Happy path: `mergeConfigs` — global `Docker` preserved when project is nil
  - Happy path: `KRNG_DOCKER=1` enables Docker mode via env override
  - Edge case: `KRNG_DOCKER=0` explicitly disables Docker mode even when config enables it

  **Verification:**
  - `make test` passes with new test cases

- [ ] **Unit 2: Mount `/sys` and share cgroup namespace when Docker is enabled**

  **Goal:** When `docker = true`, mount `/sys` read-only and switch from `--unshare-all` to individual unshare flags (omitting `--unshare-cgroup`).

  **Requirements:** R1, R2, R4

  **Dependencies:** Unit 1

  **Files:**
  - Modify: `sandbox.go` (function `buildBwrapArgs`)
  - Test: `sandbox_test.go`

  **Approach:**
  - Add `/sys` section after the `/proc` and `/dev` block: when Docker is enabled, append `--ro-bind /sys /sys`
  - Replace the namespace isolation block: when Docker is enabled, emit individual flags (`--unshare-pid`, `--unshare-ipc`, `--unshare-uts`, `--unshare-user-try`) instead of `--unshare-all`. Keep `--share-net` and `--die-with-parent` in both paths.
  - Docker socket: auto-detect `/run/docker.sock` (resolve symlinks), append `--bind <real-path> <real-path>` when the socket exists. If the socket doesn't exist, skip silently (user may use TCP Docker).

  **Patterns to follow:**
  - `shareTmp` conditional block in `buildBwrapArgs` for the flag-gated mount pattern

  **Test scenarios:**
  - Happy path: Docker enabled → args contain `--ro-bind /sys /sys`
  - Happy path: Docker enabled → args contain `--unshare-pid`, `--unshare-ipc`, `--unshare-uts` but NOT `--unshare-all` and NOT `--unshare-cgroup`
  - Happy path: Docker disabled (default) → args contain `--unshare-all` and do NOT contain `--ro-bind /sys /sys`
  - Happy path: Docker enabled → args contain `--share-net` and `--die-with-parent` (unchanged)
  - Integration: Docker enabled with socket present → args contain `--bind <socket-path> <socket-path>`
  - Edge case: Docker enabled but no socket at `/run/docker.sock` → no socket bind, no error

  **Verification:**
  - `make test` passes; Docker-enabled args match expected shape

- [ ] **Unit 3: Mount conflict detection for built-in paths**

  **Goal:** Reject `ro_bind` and `bind` config entries that would overwrite built-in mounts (`/proc`, `/dev`, `/run`, `/sys`, user's home dir) with a clear error.

  **Requirements:** R3

  **Dependencies:** Unit 1 (needs to know if Docker is enabled to include `/sys` in the protected list)

  **Files:**
  - Modify: `sandbox.go` (function `buildBwrapArgs`, before processing `cfg.ROBind`/`cfg.RWBind`)
  - Test: `sandbox_test.go`

  **Approach:**
  - Before iterating over `cfg.ROBind` and `cfg.RWBind`, build a set of protected mount points: `/proc`, `/dev`, `/run`, home dir. Add `/sys` when Docker is enabled.
  - For each config bind entry, resolve its absolute path and check if it equals any protected path. If so, return an error like `ro_bind "/dev": conflicts with built-in mount; use sub-paths instead (e.g. "/dev/snd")`
  - Sub-paths (e.g. `/run/docker.sock`, `/dev/snd`) are fine — only exact matches are rejected

  **Patterns to follow:**
  - `validateJailDir` for the error-returning validation pattern

  **Test scenarios:**
  - Error path: `ro_bind = ["/dev"]` → returns error mentioning built-in mount conflict
  - Error path: `ro_bind = ["/proc"]` → returns error
  - Error path: `bind = ["/run"]` → returns error
  - Happy path: `bind = ["/run/docker.sock"]` → allowed (sub-path, not exact match)
  - Happy path: `ro_bind = ["/dev/snd"]` → allowed
  - Edge case: Docker enabled + `ro_bind = ["/sys"]` → returns error
  - Edge case: Docker disabled + `ro_bind = ["/sys"]` → allowed (not a built-in mount when Docker is off)

  **Verification:**
  - `make test` passes; conflicting mounts are rejected, sub-paths are allowed

- [ ] **Unit 4: Update documentation and usage**

  **Goal:** Document Docker support in README, example config, and usage output.

  **Requirements:** R1, R2

  **Dependencies:** Units 1-3

  **Files:**
  - Modify: `README.md`
  - Modify: `examples/config.toml`
  - Modify: `main.go` (usage string — add `KRNG_DOCKER` env var)

  **Approach:**
  - Add Docker section to README explaining setup (`docker = true` in config)
  - Add `KRNG_DOCKER` to the environment overrides section in `usage()`
  - Uncomment/expand Docker example in `examples/config.toml`

  **Test expectation:** none — documentation only

  **Verification:**
  - `krng --help` shows `KRNG_DOCKER` env var

## System-Wide Impact

- **Interaction graph:** `buildBwrapArgs` is the only function affected. `main.go` calls it once. No callbacks or observers.
- **Error propagation:** Mount conflict errors propagate up through `buildBwrapArgs` → `main.go` → `fatalCode(exitSandbox, ...)`. Same pattern as existing errors.
- **State lifecycle risks:** None — krng is stateless (builds args and execs bwrap).
- **API surface parity:** `KRNG_DOCKER` env override follows the same pattern as `KRNG_SHARE_TMP` and `KRNG_NEW_SESSION`.
- **Unchanged invariants:** Default sandbox behavior (no Docker flag set) produces identical bwrap args as before. All existing tests must continue passing without modification.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Docker socket group permissions — user may not be in `docker` group | Not a krng issue; same as running Docker outside sandbox. Document in README. |
| Sharing cgroup namespace widens attack surface | Only enabled when `docker = true`; documented trade-off. Default remains `--unshare-all`. |
| Future bwrap versions may add `--share-cgroup` | If added, switch to `--unshare-all --share-net --share-cgroup` for cleaner args. No functional change needed. |

## Sources & References

- Related code: `sandbox.go::buildBwrapArgs`
- bwrap docs: `bwrap --help` (no `--share-cgroup` flag confirmed)
- Docker socket default: `unix:///var/run/docker.sock` (symlink to `/run/docker.sock` on systemd systems)
