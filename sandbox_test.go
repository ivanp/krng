package main

import (
	"os"
	"path/filepath"
	"testing"
)

// ── validateJailDir ───────────────────────────────────────────────────────────

func TestValidateJailDir(t *testing.T) {
	t.Parallel()

	home, _ := os.UserHomeDir()
	// Use a subdir of home as the "valid" test case because /tmp is forbidden.
	validDir := home

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid home dir", validDir, false},
		{"root", "/", true},
		{"exact /etc", "/etc", true},
		{"under /etc", "/etc/ssl", true},
		{"exact /usr", "/usr", true},
		{"under /usr", "/usr/bin", true},
		{"exact /bin", "/bin", true},
		{"under /bin", "/bin/sh", true},
		{"exact /proc", "/proc", true},
		{"under /proc", "/proc/1", true},
		{"exact /dev", "/dev", true},
		{"exact /sys", "/sys", true},
		{"exact /run", "/run", true},
		{"exact /tmp", "/tmp", true},
		// /home and /root: exact-only; subdirectories are valid jail targets
		{"exact /home", "/home", true},
		{"exact /root", "/root", true},
		// /var and /opt: exact-only; subdirs like /var/lib/myapp may be valid jails
		{"exact /var", "/var", true},
		{"exact /opt", "/opt", true},
		// /boot: prefix-blocked; no subdirectory is a valid jail target
		{"exact /boot", "/boot", true},
		{"under /boot", "/boot/efi", true},
		// /usr2 must NOT be caught by the /usr prefix check
		// (only testable if /usr2 exists, so we skip that case here)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateJailDir(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateJailDir(%q) error=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// ── mergeConfigs ─────────────────────────────────────────────────────────────

func TestMergeConfigs(t *testing.T) {
	t.Parallel()

	trueVal := true
	falseVal := false

	t.Run("global slices are preserved unchanged", func(t *testing.T) {
		t.Parallel()
		global := Config{PassEnv: []string{"A", "B"}, ROBind: []string{"/g1"}}
		got := mergeConfigs(global, ProjectConfig{})
		if len(got.PassEnv) != 2 || got.PassEnv[0] != "A" || got.PassEnv[1] != "B" {
			t.Errorf("PassEnv = %v, want [A B]", got.PassEnv)
		}
		if len(got.ROBind) != 1 || got.ROBind[0] != "/g1" {
			t.Errorf("ROBind = %v, want [/g1]", got.ROBind)
		}
	})

	t.Run("project NewSession wins when set", func(t *testing.T) {
		t.Parallel()
		global := Config{NewSession: &trueVal}
		project := ProjectConfig{NewSession: &falseVal}
		got := mergeConfigs(global, project)
		if got.NewSession == nil || *got.NewSession != false {
			t.Errorf("NewSession = %v, want false", got.NewSession)
		}
	})

	t.Run("global NewSession preserved when project nil", func(t *testing.T) {
		t.Parallel()
		global := Config{NewSession: &trueVal}
		got := mergeConfigs(global, ProjectConfig{})
		if got.NewSession == nil || *got.NewSession != true {
			t.Errorf("NewSession = %v, want true", got.NewSession)
		}
	})

	t.Run("project ShareTmp wins when set", func(t *testing.T) {
		t.Parallel()
		global := Config{ShareTmp: &falseVal}
		project := ProjectConfig{ShareTmp: &trueVal}
		got := mergeConfigs(global, project)
		if got.ShareTmp == nil || *got.ShareTmp != true {
			t.Errorf("ShareTmp = %v, want true", got.ShareTmp)
		}
	})

	t.Run("zero project config leaves global unchanged", func(t *testing.T) {
		t.Parallel()
		got := mergeConfigs(Config{}, ProjectConfig{})
		if got.NewSession != nil || got.ShareTmp != nil {
			t.Errorf("expected nil pointer fields, got NewSession=%v ShareTmp=%v", got.NewSession, got.ShareTmp)
		}
		if len(got.PassEnv) != 0 || len(got.ROBind) != 0 || len(got.RWBind) != 0 {
			t.Errorf("expected all empty slices, got %+v", got)
		}
	})
}

// ── buildBwrapArgs: HOME ──────────────────────────────────────────────────────

func TestBuildBwrapArgsHome(t *testing.T) {
	t.Parallel()

	home, _ := os.UserHomeDir()

	args, err := buildBwrapArgs(home, Config{}, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	assertEnv(t, args, "HOME", home)
	assertTmpfs(t, args, home)
}

func TestTmpfsHomeBeforeChildBinds(t *testing.T) {
	t.Parallel()

	home, _ := os.UserHomeDir()
	childPath := filepath.Join(home, ".claude")
	cfg := Config{RWBind: []string{childPath}}

	args, err := buildBwrapArgs(home, cfg, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}

	tmpfsIdx := -1
	bindIdx := -1
	for i, a := range args {
		if a == "--tmpfs" && i+1 < len(args) && args[i+1] == home {
			tmpfsIdx = i
		}
		if a == "--bind" && i+1 < len(args) && args[i+1] == childPath {
			bindIdx = i
		}
	}
	if tmpfsIdx == -1 {
		t.Fatal("--tmpfs home not found in args")
	}
	if bindIdx == -1 {
		t.Fatal("--bind child not found in args")
	}
	if tmpfsIdx > bindIdx {
		t.Errorf("--tmpfs home (idx %d) must come before --bind child (idx %d)", tmpfsIdx, bindIdx)
	}
}

func TestBuildBwrapArgsKrngTomlROBind(t *testing.T) {
	t.Parallel()

	// With krng.toml present: file must be bound read-only.
	dir, err := os.MkdirTemp("", "krng-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	tomlPath := filepath.Join(dir, "krng.toml")
	if err := os.WriteFile(tomlPath, []byte("# empty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	args, err := buildBwrapArgs(dir, Config{}, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	assertROBind(t, args, tomlPath)

	// Without krng.toml: no extra ro-bind for that path.
	dir2, err := os.MkdirTemp("", "krng-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir2)

	args2, err := buildBwrapArgs(dir2, Config{}, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	toml2Path := filepath.Join(dir2, "krng.toml")
	for i := 0; i+2 < len(args2); i++ {
		if args2[i] == "--ro-bind" && args2[i+1] == toml2Path {
			t.Errorf("unexpected --ro-bind %s when krng.toml does not exist", toml2Path)
		}
	}
}

// assertROBind scans bwrap args for --ro-bind PATH PATH and fails if not found.
func assertROBind(t *testing.T, args []string, path string) {
	t.Helper()
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--ro-bind" && args[i+1] == path && args[i+2] == path {
			return
		}
	}
	t.Errorf("--ro-bind %s %s not found in args", path, path)
}

// assertTmpfs scans bwrap args for --tmpfs PATH and fails if not found.
func assertTmpfs(t *testing.T, args []string, path string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tmpfs" && args[i+1] == path {
			return
		}
	}
	t.Errorf("--tmpfs %s not found in args", path)
}

// assertEnv scans a bwrap args slice for --setenv KEY VALUE and checks the value.
func assertEnv(t *testing.T, args []string, key, want string) {
	t.Helper()
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--setenv" && args[i+1] == key {
			if args[i+2] != want {
				t.Errorf("--setenv %s = %q, want %q", key, args[i+2], want)
			}
			return
		}
	}
	t.Errorf("--setenv %s not found in args", key)
}

// ── absPath ───────────────────────────────────────────────────────────────────

func TestAbsPath(t *testing.T) {
	t.Parallel()

	home, _ := os.UserHomeDir()

	t.Run("absolute path unchanged", func(t *testing.T) {
		t.Parallel()
		got, err := absPath(home)
		if err != nil || got != home {
			t.Errorf("absPath(%q) = %q, %v", home, got, err)
		}
	})

	t.Run("tilde expanded to home", func(t *testing.T) {
		t.Parallel()
		got, err := absPath("~")
		if err != nil || got != home {
			t.Errorf("absPath(~) = %q, %v", got, err)
		}
	})

	t.Run("symlink resolved", func(t *testing.T) {
		t.Parallel()
		dir, err := os.MkdirTemp("", "krng-abs-*")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		link := filepath.Join(dir, "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}
		got, absErr := absPath(link)
		if absErr != nil || got != dir {
			t.Errorf("absPath(symlink) = %q, %v; want %q", got, absErr, dir)
		}
	})

	t.Run("non-existent path rejected", func(t *testing.T) {
		t.Parallel()
		_, err := absPath("/nonexistent/jailwrap/path")
		if err == nil {
			t.Error("absPath(/nonexistent/jailwrap/path) expected error, got nil")
		}
	})

	t.Run("relative path rejected", func(t *testing.T) {
		t.Parallel()
		_, err := absPath("relative/path")
		if err == nil {
			t.Error("absPath(relative/path) expected error, got nil")
		}
	})

	t.Run("bare filename rejected", func(t *testing.T) {
		t.Parallel()
		_, err := absPath("file.txt")
		if err == nil {
			t.Error("absPath(file.txt) expected error, got nil")
		}
	})
}
