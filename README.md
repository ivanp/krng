# krng

**krng** (*kurung* — Malay/Indonesian for "to confine") is a lightweight sandbox wrapper for developer tools on Linux. It uses [Bubblewrap](https://github.com/containers/bubblewrap) to confine tools (e.g. AI coding assistants, build systems) to a project directory, isolating them from credentials, SSH keys, GPG sockets, and other sensitive data on the host.

## Why

AI coding assistants and other developer tools run arbitrary commands. Sandboxing them limits what they can read or exfiltrate — without the overhead of a full VM or container runtime.

## Requirements

- Linux (kernel 5.4+ recommended)
- [`bwrap`](https://github.com/containers/bubblewrap) — install via your package manager:
  ```sh
  # Arch
  sudo pacman -S bubblewrap

  # Debian/Ubuntu
  sudo apt install bubblewrap

  # Fedora
  sudo dnf install bubblewrap
  ```

## Installation

### From source

```sh
git clone https://github.com/ivanp/krng
cd krng

# Install to /usr/local/bin (requires write permission)
sudo make install

# Or install to ~/.local/bin (no sudo)
make install-local
```

The binary is statically compiled (`CGO_ENABLED=0`) with no runtime dependencies.

### Verify

```sh
krng --version
```

## Usage

```sh
krng [WORK_DIR] COMMAND [ARGS...]
```

- **`WORK_DIR`** — optional absolute path to the project directory. Defaults to the current working directory.
- **`COMMAND`** — the program to run inside the sandbox.

### Examples

```sh
# Run bash in the current directory
krng bash

# Run a tool in a specific project
krng /home/user/myproject claude --dangerously-skip-permissions

# Pass environment variables into the sandbox
KRNG_PASSENV=ANTHROPIC_API_KEY krng claude
```

## What the sandbox provides

| Resource | Behavior |
|---|---|
| Project directory (`WORK_DIR`) | Read-write |
| `/usr`, `/bin`, `/lib`, `/lib64` | Read-only |
| `/etc/resolv.conf`, `/etc/hosts`, etc. | Read-only (no `/etc/passwd` secrets) |
| `/etc/ssl/certs`, CA certificates | Read-only |
| `/run` | Isolated tmpfs (no GPG/DBUS/keyring sockets) |
| `/tmp` | Isolated tmpfs (no X11 sockets) — see `share_tmp` |
| `/proc`, `/dev` | Virtualised |
| Network | Shared with host |
| `HOME` | Real user home directory, mounted read-only (write attempts fail) |
| SSH keys, GPG keys, credentials | Not accessible |
| `SSH_AUTH_SOCK`, `DBUS_SESSION_BUS_ADDRESS`, `AWS_*`, `GH_TOKEN`, etc. | Cleared (`--clearenv`) |

## Configuration

krng uses two levels of TOML configuration:

### Global config

`~/.config/krng/config.toml` (or `$XDG_CONFIG_HOME/krng/config.toml`)

Created automatically on first run. Settings here apply to every invocation.

```toml
# Pass API keys into the sandbox
passenv = [
  "ANTHROPIC_API_KEY",
  "OPENAI_API_KEY",
]

# Pass the host PATH so tools in custom directories (e.g. ~/.local/bin, ~/.dual-graph)
# are found inside the sandbox. Without this, the sandbox uses a minimal system PATH.
# passenv = ["PATH"]

# Read-only bind mounts
ro_bind = [
  "~/.config/gh",       # GitHub CLI config
]

# Read-write bind mounts
# bind = [
#   "/var/run/docker.sock",
# ]

# Disable --new-session (re-enables Ctrl+Z / job control)
# new_session = false

# Share host /tmp (exposes X11/Wayland sockets — off by default)
# share_tmp = true
```

### Per-project config

`WORK_DIR/krng.toml`

Per-project config is intentionally **restricted to behavior flags only** (`new_session`, `share_tmp`). Bind mounts and env pass-through are not allowed here — those require the global config, which only you control.

> ⚠ **Security:** `krng.toml` is loaded from the project directory before sandboxing. A malicious repository can use it to change sandbox behavior. Review it before running krng in an untrusted repository.

```toml
# Disable --new-session to re-enable Ctrl+Z / job control.
# new_session = false

# Share host /tmp (exposes X11/Wayland sockets).
# share_tmp = true
```

### Config merge rules

- **Bind mounts and passenv** (`passenv`, `ro_bind`, `bind`): global config only. Project config cannot declare these.
- **Booleans** (`new_session`, `share_tmp`): project value overrides global if set; global value used otherwise.

### Environment overrides

| Variable | Effect |
|---|---|
| `KRNG_PASSENV` | Comma-separated env vars to pass through |
| `KRNG_SHARE_TMP` | `1` = share host `/tmp`; `0` = isolate |
| `KRNG_NEW_SESSION` | `0` = disable `--new-session`; `1` = enable |

## Security notes

**`krng.toml` in a project directory you did not write can change sandbox behavior.** Review `krng.toml` before running krng in an untrusted repository, just as you would review a `Makefile` or `package.json`.

**krng is not a complete security boundary.** It reduces attack surface but does not prevent all possible escapes. For stronger isolation, consider a VM.

**`--new-session`** is enabled by default. It detaches the sandboxed process from the controlling TTY, mitigating CVE-2017-5226 (TIOCSTI injection) and CVE-2025-37814. Disable it with `new_session = false` if you need `Ctrl+Z` / `fg` job control inside the sandbox.

**`KRNG_ACTIVE`** must not be set in shell profiles (`~/.bashrc`, `~/.zshrc`, etc.). If set outside a sandbox, sandboxing is skipped entirely and the command runs with the full parent environment.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Usage error (no arguments) |
| 2 | Invalid arguments or command not found |
| 3 | Config error (parse failure or unreadable config directory) |
| 4 | Invalid WORK_DIR (protected path or unresolvable symlink) |
| 6 | `bwrap` not found or exec failed |
| 7 | Error constructing sandbox arguments |

## Building

```sh
make build       # compile
make test        # run tests with race detector
make check       # vet + test
make clean       # remove build artifacts
```

## License

MIT — see [LICENSE](LICENSE).
