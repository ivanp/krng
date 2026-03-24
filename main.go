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

// Exit codes. Orchestrators can use these to distinguish failure modes without
// parsing stderr strings.
const (
	exitArgs    = 2 // invalid arguments or missing command
	exitConfig  = 3 // config parse or creation error
	exitJailDir = 4 // validateJailDir rejected the path
	exitBwrap   = 6 // bwrap not found or exec failed
	exitSandbox = 7 // buildBwrapArgs error
)

func fatalCode(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "krng: "+format+"\n", args...)
	os.Exit(code)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  krng [WORK_DIR] COMMAND [ARGS...]

Runs COMMAND inside a bwrap sandbox rooted at WORK_DIR (default: current directory).
The sandbox has read-only access to system paths and read-write access only to WORK_DIR.

Arguments:
  WORK_DIR   Optional absolute path to the project directory to sandbox.
             Defaults to the current working directory.
  COMMAND    The program to run inside the sandbox.
  ARGS       Arguments passed to COMMAND.

Options:
  -h, --help       Show this help message.
  --version        Show version information.

Configuration:
  Global config:   ~/.config/krng/config.toml  (or $XDG_CONFIG_HOME/krng/config.toml)
  Project config:  WORK_DIR/krng.toml

Environment overrides:
  KRNG_PASSENV      Comma-separated env vars to pass through (e.g. "FOO,BAR")
  KRNG_SHARE_TMP    "1" to share host /tmp; "0" to isolate
  KRNG_NEW_SESSION  "0" to disable --new-session (re-enables job control)

Exit codes:
  0   Success
  1   Usage error (no arguments)
  2   Invalid arguments or command not found
  3   Config error (parse failure or missing config directory)
  4   Invalid WORK_DIR (protected path or symlink attack)
  6   bwrap not found or exec failed
  7   Error constructing sandbox arguments

Examples:
  krng bash
  krng /home/user/myproject claude --dangerously-skip-permissions
  KRNG_PASSENV=ANTHROPIC_API_KEY krng claude
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
		fmt.Printf("krng %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	// Nested sandbox detection: KRNG_ACTIVE is set via --setenv in buildBwrapArgs
	// because --clearenv removes it from the child environment.
	// WARNING: do not set KRNG_ACTIVE in shell profiles (~/.bashrc, ~/.zshrc, etc.)
	// — if set outside a sandbox, sandboxing is skipped entirely.
	if os.Getenv("KRNG_ACTIVE") != "" {
		// Peek at args to detect WORK_DIR (for the warning message only).
		jailDirArg := ""
		if len(args) > 1 && filepath.IsAbs(args[0]) {
			if info, statErr := os.Stat(args[0]); statErr == nil && info.IsDir() {
				jailDirArg = args[0]
				args = args[1:]
			}
		}
		if jailDirArg != "" {
			fmt.Fprintf(os.Stderr,
				"krng: warning: already inside a krng sandbox; ignoring WORK_DIR %q and running %q directly\n",
				jailDirArg, args[0])
		} else {
			fmt.Fprintf(os.Stderr,
				"krng: warning: already inside a krng sandbox; running %q directly\n",
				args[0])
		}
		// exec the command directly — no double-wrapping
		cmdPath, err := exec.LookPath(args[0])
		if err != nil {
			fatalCode(exitArgs, "command not found: %s", args[0])
		}
		if !filepath.IsAbs(cmdPath) {
			fatalCode(exitBwrap, "exec.LookPath returned non-absolute path %q", cmdPath)
		}
		os.Stderr.Sync()
		if err := syscall.Exec(cmdPath, args, os.Environ()); err != nil {
			fatalCode(exitBwrap, "exec %s: %v", cmdPath, err)
		}
	}

	// Determine WORK_DIR: if first arg is an existing absolute directory, use it.
	jailDir, err := os.Getwd()
	if err != nil {
		fatalCode(exitArgs, "getwd: %v", err)
	}
	if filepath.IsAbs(args[0]) {
		if info, err := os.Stat(args[0]); err == nil && info.IsDir() {
			jailDir = filepath.Clean(args[0])
			args = args[1:]
			if len(args) == 0 {
				fatalCode(exitArgs, "no command specified after WORK_DIR")
			}
		}
		// Non-directory absolute path: treat as command (fall through)
	}

	// ── Config loading order ────────────────────────────────────────────────
	// MUST be: load global → validateJailDir → load project config.
	// Running `krng /etc` must not read /etc/krng.toml before the check fires.

	configDir, err := os.UserConfigDir()
	if err != nil {
		fatalCode(exitConfig, "cannot determine config directory: %v", err)
	}
	globalConfigPath := filepath.Join(configDir, "krng", "config.toml")

	// First-run: create default config if it doesn't exist.
	if err := createDefaultConfig(globalConfigPath); err != nil {
		// Non-fatal: warn and proceed; don't break the user's workflow.
		fmt.Fprintf(os.Stderr, "krng: warning: could not create default config: %v\n", err)
	}

	globalCfg, err := loadConfig(globalConfigPath)
	if err != nil {
		fatalCode(exitConfig, "%v", err)
	}

	// Validate WORK_DIR before loading per-project config.
	jailDir, err = validateJailDir(jailDir)
	if err != nil {
		fatalCode(exitJailDir, "%v", err)
	}

	projectCfg, err := loadProjectConfig(filepath.Join(jailDir, "krng.toml"))
	if err != nil {
		fatalCode(exitConfig, "%v", err)
	}

	cfg := mergeConfigs(globalCfg, projectCfg)
	cfg = applyEnvOverrides(cfg)

	bwrapArgs, err := buildBwrapArgs(jailDir, cfg, os.Getuid())
	if err != nil {
		fatalCode(exitSandbox, "build sandbox args: %v", err)
	}

	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		fatalCode(exitBwrap, "bwrap not found in PATH: %v\nInstall bubblewrap: https://github.com/containers/bubblewrap", err)
	}
	// Guard against PATH injection: LookPath should always return an absolute path,
	// but verify explicitly.
	if !filepath.IsAbs(bwrapPath) {
		fatalCode(exitBwrap, "exec.LookPath(\"bwrap\") returned non-absolute path %q", bwrapPath)
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
		fatalCode(exitBwrap, "exec bwrap: %v", err)
	}
}
