package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// validateJailDir resolves symlinks and checks that jailDir is not a protected
// system path. Must be called BEFORE loading the per-project config so that
// running `jailwrap /etc` cannot read /etc/jailwrap.toml before the check fires.
func validateJailDir(raw string) (string, error) {
	// Resolve symlinks so a symlink at /home/user/link → /etc bypasses nothing.
	// EvalSymlinks resolves symlinks and returns a clean absolute path.
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", fmt.Errorf("JAIL_DIR %q: %w", raw, err)
	}

	// Exact match for filesystem root (must be separate: "/" is a prefix of everything).
	if resolved == "/" {
		return "", fmt.Errorf("JAIL_DIR %q is a protected system path", resolved)
	}

	// Prefix-blocked directories: no subdirectory is a valid jail target.
	// Note: trailing slash in the prefix check prevents /usr2 being caught by /usr.
	prefixForbidden := []string{
		"/usr", "/bin", "/lib", "/lib64",
		"/etc", "/run", "/proc", "/dev", "/sys", "/tmp",
		"/boot",
	}
	for _, f := range prefixForbidden {
		if resolved == f || strings.HasPrefix(resolved, f+"/") {
			return "", fmt.Errorf("JAIL_DIR %q is a protected system path", resolved)
		}
	}

	// Exact-blocked directories: the root itself is forbidden but subdirectories
	// may be valid jail targets (e.g. /home/user/project, /var/lib/myapp).
	exactForbidden := []string{"/home", "/root", "/var", "/opt"}
	for _, f := range exactForbidden {
		if resolved == f {
			return "", fmt.Errorf("JAIL_DIR %q is a protected system path", resolved)
		}
	}
	return resolved, nil
}

// buildBwrapArgs constructs the bwrap argument list from jailDir, cfg and uid.
// Any error in path resolution is returned immediately — never silently omit a requested mount.
func buildBwrapArgs(jailDir string, cfg Config, uid int) ([]string, error) {
	uidStr := strconv.Itoa(uid)
	var args []string

	// ── Filesystem baseline ────────────────────────────────────────────────────

	args = append(args, "--ro-bind", "/usr", "/usr")

	// Merged-usr vs traditional distros:
	// On merged-usr (Arch, Fedora 17+, Debian 11+, Ubuntu 20.10+), /bin, /lib, /sbin
	// are symlinks to usr/bin, usr/lib, usr/sbin — create matching symlinks in container.
	// On traditional distros (Debian ≤10, Alpine, Void) they are real dirs — bind them.
	for _, e := range []struct{ dir, target string }{
		{"/bin", "usr/bin"},
		{"/lib", "usr/lib"},
		{"/sbin", "usr/sbin"},
	} {
		info, err := os.Lstat(e.dir)
		if err != nil {
			continue // dir doesn't exist on this system
		}
		if info.Mode()&os.ModeSymlink != 0 {
			args = append(args, "--symlink", e.target, e.dir)
		} else {
			args = append(args, "--ro-bind", e.dir, e.dir)
		}
	}

	// /lib64: conditional on host existence
	if _, err := os.Stat("/lib64"); err == nil {
		args = append(args, "--ro-bind", "/lib64", "/lib64")
	}

	// ── Selective /etc (no credentials) ───────────────────────────────────────

	// /etc/resolv.conf: on systemd-resolved systems this is a symlink into
	// /run/systemd/resolve/stub-resolv.conf. When /run is a tmpfs, the symlink
	// becomes dangling and DNS breaks. Resolve the real path and bind that directly.
	resolv := "/etc/resolv.conf"
	if real, err := filepath.EvalSymlinks(resolv); err == nil {
		resolv = real
	}
	args = append(args, "--ro-bind", resolv, "/etc/resolv.conf")

	for _, f := range []string{
		"/etc/hosts",
		"/etc/nsswitch.conf",
		"/etc/localtime",
		"/etc/passwd",
		"/etc/group",
	} {
		if _, err := os.Stat(f); err == nil {
			args = append(args, "--ro-bind", f, f)
		}
	}
	for _, d := range []string{"/etc/ssl/certs", "/etc/ca-certificates"} {
		if _, err := os.Stat(d); err == nil {
			args = append(args, "--ro-bind", d, d)
		}
	}

	// ── Runtime: isolated (no GPG/DBUS/keyring sockets) ───────────────────────

	// --ro-bind /run would expose credential-bearing sockets even read-only
	// (socket connections ignore mount RO flags). Use tmpfs instead.
	args = append(args,
		"--tmpfs", "/run",
		"--dir", "/run/user/"+uidStr,
	)

	// ── Proc and dev ───────────────────────────────────────────────────────────

	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
	)

	// ── /tmp: isolated by default, shared on request ───────────────────────────

	// Do NOT do --tmpfs /tmp followed by --bind /tmp /tmp: bwrap processes mounts
	// left-to-right and the bind would overwrite the tmpfs, sharing host /tmp and
	// exposing X11 sockets, other tools' session files, etc.
	shareTmp := cfg.ShareTmp != nil && *cfg.ShareTmp
	if shareTmp {
		args = append(args, "--bind", "/tmp", "/tmp")
	} else {
		args = append(args, "--tmpfs", "/tmp")
	}

	// ── Project directory (writable) ───────────────────────────────────────────

	args = append(args,
		"--bind", jailDir, jailDir,
		"--chdir", jailDir,
	)

	// Lock jailwrap.toml read-only inside the sandbox so a sandboxed process
	// cannot modify it to expand its own privileges on the next invocation.
	// The --ro-bind here is more specific than the --bind jailDir above, so
	// bwrap shadows the writable directory mount with a read-only file mount.
	projectCfgPath := filepath.Join(jailDir, "jailwrap.toml")
	if _, err := os.Stat(projectCfgPath); err == nil {
		args = append(args, "--ro-bind", projectCfgPath, projectCfgPath)
	}

	// ── Namespace isolation ────────────────────────────────────────────────────

	args = append(args,
		"--unshare-all",
		"--share-net",
		"--die-with-parent",
	)

	// --new-session: detaches from controlling TTY, mitigating CVE-2017-5226
	// (TIOCSTI injection) and CVE-2025-37814. Default on; opt-out for interactive shells.
	newSession := cfg.NewSession == nil || *cfg.NewSession
	if newSession {
		args = append(args, "--new-session")
	}

	// ── Config-declared extra mounts ───────────────────────────────────────────

	for _, p := range cfg.ROBind {
		abs, err := absPath(p)
		if err != nil {
			return nil, fmt.Errorf("ro_bind %q: %w", p, err)
		}
		args = append(args, "--ro-bind", abs, abs)
	}
	for _, p := range cfg.RWBind {
		abs, err := absPath(p)
		if err != nil {
			return nil, fmt.Errorf("bind %q: %w", p, err)
		}
		args = append(args, "--bind", abs, abs)
	}

	// ── Clean environment with explicit allowlist ──────────────────────────────

	// --clearenv drops the entire parent environment. We then --setenv only the safe
	// subset. This prevents SSH_AUTH_SOCK, DBUS_SESSION_BUS_ADDRESS, AWS_*, GH_TOKEN,
	// ANTHROPIC_API_KEY, etc. from leaking into the sandbox.
	args = append(args, "--clearenv")

	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("LOGNAME")
	}
	term := os.Getenv("TERM")
	if term == "" {
		term = "xterm-256color"
	}
	colorterm := os.Getenv("COLORTERM")
	lang := os.Getenv("LANG")
	if lang == "" {
		lang = "en_US.UTF-8"
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving user home: %w", err)
	}

	args = append(args,
		"--setenv", "HOME", homeDir,
		"--setenv", "TMPDIR", "/tmp",
		"--setenv", "PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"--setenv", "USER", user,
		"--setenv", "LOGNAME", user,
		"--setenv", "SHELL", "/bin/bash",
		"--setenv", "TERM", term,
		"--setenv", "LANG", lang,
		"--setenv", "XDG_RUNTIME_DIR", "/run/user/"+uidStr,
		// Must be explicit: bwrap --clearenv removes it from the child env,
		// so nested jailwrap detection only works if we set it here.
		"--setenv", "JAILWRAP_ACTIVE", "1",
	)

	// COLORTERM signals true-color support (e.g. "truecolor", "24bit").
	// Pass it through if set so TUI apps like Claude Code render colors correctly.
	if colorterm != "" {
		args = append(args, "--setenv", "COLORTERM", colorterm)
	}

	// PassEnv: pass named variables from the parent environment into the sandbox.
	for _, name := range cfg.PassEnv {
		if val, ok := os.LookupEnv(name); ok {
			args = append(args, "--setenv", name, val)
		}
	}

	return args, nil
}
