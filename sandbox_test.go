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
		// /usr2 must NOT be caught by the /usr prefix check
		// (only testable if /usr2 exists, so we skip that case here)
	}

	for _, tt := range tests {
		tt := tt
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

// ── expandHome ───────────────────────────────────────────────────────────────

func TestExpandHome(t *testing.T) {
	t.Parallel()

	home, _ := os.UserHomeDir()

	tests := []struct {
		input string
		want  string
	}{
		{"~", home},
		{"~/foo/bar", home + "/foo/bar"},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := expandHome(tt.input)
			if err != nil {
				t.Fatalf("expandHome(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
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
}

func TestBuildBwrapArgsJailwrapTomlROBind(t *testing.T) {
	t.Parallel()

	// With jailwrap.toml present: file must be bound read-only.
	dir, err := os.MkdirTemp("", "jailwrap-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	tomlPath := filepath.Join(dir, "jailwrap.toml")
	if err := os.WriteFile(tomlPath, []byte("# empty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	args, err := buildBwrapArgs(dir, Config{}, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	assertROBind(t, args, tomlPath)

	// Without jailwrap.toml: no extra ro-bind for that path.
	dir2, err := os.MkdirTemp("", "jailwrap-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir2)

	args2, err := buildBwrapArgs(dir2, Config{}, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	toml2Path := filepath.Join(dir2, "jailwrap.toml")
	for i := 0; i+2 < len(args2); i++ {
		if args2[i] == "--ro-bind" && args2[i+1] == toml2Path {
			t.Errorf("unexpected --ro-bind %s when jailwrap.toml does not exist", toml2Path)
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
		got, err := absPath("/usr/local/bin")
		if err != nil || got != "/usr/local/bin" {
			t.Errorf("absPath(/usr/local/bin) = %q, %v", got, err)
		}
	})

	t.Run("tilde expanded to absolute", func(t *testing.T) {
		t.Parallel()
		got, err := absPath("~/foo")
		if err != nil || got != home+"/foo" {
			t.Errorf("absPath(~/foo) = %q, %v", got, err)
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
