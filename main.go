package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "jailwrap: "+format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  jailwrap [JAIL_DIR] COMMAND [ARGS...]

Runs COMMAND inside a bwrap sandbox rooted at JAIL_DIR (default: current directory).
The sandbox has read-only access to system paths and read-write access only to JAIL_DIR.

Arguments:
  JAIL_DIR   Optional absolute path to the project directory to sandbox.
             Defaults to the current working directory.
  COMMAND    The program to run inside the sandbox.
  ARGS       Arguments passed to COMMAND.

Options:
  -h, --help       Show this help message.
  --version        Show version information.

Configuration:
  Global config:   ~/.config/jailwrap/config.toml  (or $XDG_CONFIG_HOME/jailwrap/config.toml)
  Project config:  JAIL_DIR/jailwrap.toml

Environment overrides:
  JAILWRAP_PASSENV      Comma-separated env vars to pass through (e.g. "FOO,BAR")
  JAILWRAP_SHARE_TMP    "1" to share host /tmp; "0" to isolate
  JAILWRAP_NEW_SESSION  "0" to disable --new-session (re-enables job control)

Examples:
  jailwrap bash
  jailwrap /home/user/myproject claude --dangerously-skip-permissions
  JAILWRAP_PASSENV=ANTHROPIC_API_KEY jailwrap claude
`)
}

func main() {
	args := os.Args[1:] // local copy — never mutate os.Args

	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	switch args[0] {
	case "--help", "-h":
		usage()
		os.Exit(0)
	case "--version":
		fmt.Printf("jailwrap %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	// Nested sandbox detection: JAILWRAP_ACTIVE is set via --setenv in buildBwrapArgs
	// because --clearenv removes it from the child environment.
	if os.Getenv("JAILWRAP_ACTIVE") != "" {
		fmt.Fprintf(os.Stderr, "jailwrap: warning: already inside a jailwrap sandbox; running command directly\n")
		// exec the command directly — no double-wrapping
		cmdPath, err := exec.LookPath(args[0])
		if err != nil {
			fatal("command not found: %s", args[0])
		}
		if !filepath.IsAbs(cmdPath) {
			fatal("exec.LookPath returned non-absolute path %q", cmdPath)
		}
		os.Stderr.Sync()
		if err := syscall.Exec(cmdPath, args, os.Environ()); err != nil {
			fatal("exec %s: %v", cmdPath, err)
		}
	}

	// Determine JAIL_DIR: if first arg is an existing absolute directory, use it.
	jailDir, err := os.Getwd()
	if err != nil {
		fatal("getwd: %v", err)
	}
	if filepath.IsAbs(args[0]) {
		if info, err := os.Stat(args[0]); err == nil && info.IsDir() {
			jailDir = filepath.Clean(args[0])
			args = args[1:]
			if len(args) == 0 {
				fatal("no command specified after JAIL_DIR")
			}
		}
		// Non-directory absolute path: treat as command (fall through)
	}

	// ── Config loading order ────────────────────────────────────────────────
	// MUST be: load global → validateJailDir → load project config.
	// Running `jailwrap /etc` must not read /etc/jailwrap.toml before the check fires.

	configDir, err := os.UserConfigDir()
	if err != nil {
		fatal("cannot determine config directory: %v", err)
	}
	globalConfigPath := filepath.Join(configDir, "jailwrap", "config.toml")

	// First-run: create default config if it doesn't exist.
	if err := createDefaultConfig(globalConfigPath); err != nil {
		// Non-fatal: warn and proceed; don't break the user's workflow.
		fmt.Fprintf(os.Stderr, "jailwrap: warning: could not create default config: %v\n", err)
	}

	globalCfg, err := loadConfig(globalConfigPath)
	if err != nil {
		fatal("%v", err)
	}

	// Validate JAIL_DIR before loading per-project config.
	jailDir, err = validateJailDir(jailDir)
	if err != nil {
		fatal("%v", err)
	}

	projectCfg, err := loadProjectConfig(filepath.Join(jailDir, "jailwrap.toml"))
	if err != nil {
		fatal("%v", err)
	}

	cfg := mergeConfigs(globalCfg, projectCfg)
	cfg = applyEnvOverrides(cfg)

	bwrapArgs, err := buildBwrapArgs(jailDir, cfg, os.Getuid())
	if err != nil {
		fatal("build sandbox args: %v", err)
	}

	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		fatal("bwrap not found in PATH: %v\nInstall bubblewrap: https://github.com/containers/bubblewrap", err)
	}
	// Guard against PATH injection: LookPath should always return an absolute path,
	// but verify explicitly.
	if !filepath.IsAbs(bwrapPath) {
		fatal("exec.LookPath(\"bwrap\") returned non-absolute path %q", bwrapPath)
	}

	// Build final argv: bwrap <sandbox-args> -- <command> <command-args>
	argv := make([]string, 0, 1+len(bwrapArgs)+1+len(args))
	argv = append(argv, "bwrap") // argv[0] is the program name shown in ps
	argv = append(argv, bwrapArgs...)
	argv = append(argv, "--")
	argv = append(argv, args...)

	// Minimal environment for the bwrap process itself.
	// The child process environment is controlled via --clearenv + --setenv in bwrapArgs.
	bwrapEnv := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}

	os.Stderr.Sync()
	if err := syscall.Exec(bwrapPath, argv, bwrapEnv); err != nil {
		fatal("exec bwrap: %v", err)
	}
}
