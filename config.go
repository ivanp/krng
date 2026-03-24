package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed examples
var examplesFS embed.FS

// Config is the parsed representation of the global config (~/.config/jailwrap/config.toml).
// Pointer fields allow detection of "not set" (nil) vs explicit false.
type Config struct {
	PassEnv    []string `toml:"passenv"`
	ROBind     []string `toml:"ro_bind"`
	RWBind     []string `toml:"bind"`
	NewSession *bool    `toml:"new_session"`
	ShareTmp   *bool    `toml:"share_tmp"`
}

// ProjectConfig is the parsed representation of the per-project config (JAIL_DIR/jailwrap.toml).
// It is intentionally restricted to behavior flags only — bind mounts and env pass-through are
// not permitted in project config to prevent credential exposure from untrusted repositories.
type ProjectConfig struct {
	NewSession *bool `toml:"new_session"`
	ShareTmp   *bool `toml:"share_tmp"`
}

// loadConfig loads a TOML config from path.
// Returns a zero Config (no error) if the file does not exist.
// Returns an error if the file exists but cannot be read or parsed.
func loadConfig(path string) (Config, error) {
	var cfg Config
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	decoder := toml.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// loadProjectConfig loads a per-project TOML config from path.
// Uses ProjectConfig — a restricted struct that only allows behavior flags (new_session, share_tmp).
// Unknown fields (passenv, ro_bind, bind) are rejected with an error.
// Returns a zero ProjectConfig (no error) if the file does not exist.
func loadProjectConfig(path string) (ProjectConfig, error) {
	var cfg ProjectConfig
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	decoder := toml.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// mergeConfigs merges project config on top of global config.
// Project config is restricted to behavior flags (new_session, share_tmp) —
// bind mounts and env pass-through come from the global config only.
func mergeConfigs(global Config, project ProjectConfig) Config {
	merged := global
	if project.NewSession != nil {
		merged.NewSession = project.NewSession
	}
	if project.ShareTmp != nil {
		merged.ShareTmp = project.ShareTmp
	}
	return merged
}

// absPath expands a leading ~ or ~/ to the user's home directory, verifies the
// result is absolute, and resolves symlinks. Relative paths and non-existent
// paths are rejected with a clear error rather than producing cryptic bwrap failures.
func absPath(p string) (string, error) {
	var expanded string
	switch {
	case p == "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		expanded = home
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~/: %w", err)
		}
		expanded = filepath.Join(home, p[2:])
	default:
		expanded = p
	}
	cleaned := filepath.Clean(expanded)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path %q must be absolute (no relative paths in config)", p)
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", fmt.Errorf("path %q: %w", p, err)
	}
	return resolved, nil
}

// createDefaultConfig creates the global config directory and file on first run.
// Uses O_EXCL for atomic creation — safe against concurrent jailwrap invocations
// and symlink injection attacks.
// Returns nil if the file already exists (another process beat us — that's fine).
func createDefaultConfig(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// O_EXCL: fails if file already exists (including if it's a symlink target)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil // another invocation created it first — fine
		}
		return fmt.Errorf("create config file: %w", err)
	}
	defer f.Close()

	content, err := examplesFS.ReadFile("examples/config.toml")
	if err != nil {
		return fmt.Errorf("read embedded config: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

// applyEnvOverrides applies JAILWRAP_* environment variables on top of cfg.
func applyEnvOverrides(cfg Config) Config {
	// JAILWRAP_PASSENV: comma-separated, spaces trimmed, empty tokens skipped
	if v := os.Getenv("JAILWRAP_PASSENV"); v != "" {
		for _, name := range strings.Split(v, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				cfg.PassEnv = append(cfg.PassEnv, name)
			}
		}
	}

	// JAILWRAP_SHARE_TMP: "1" enables, "0" explicitly disables
	switch os.Getenv("JAILWRAP_SHARE_TMP") {
	case "1":
		t := true
		cfg.ShareTmp = &t
	case "0":
		f := false
		cfg.ShareTmp = &f
	}

	// JAILWRAP_NEW_SESSION: "0" disables (default is on), "1" explicitly enables
	switch os.Getenv("JAILWRAP_NEW_SESSION") {
	case "0":
		f := false
		cfg.NewSession = &f
	case "1":
		t := true
		cfg.NewSession = &t
	}

	return cfg
}
