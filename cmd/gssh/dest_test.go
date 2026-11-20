package main

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestParseDestination(t *testing.T) {
	current, _ := user.Current()
	currentName := ""
	if current != nil {
		currentName = current.Username
	}

	tests := []struct {
		name     string
		dest     string
		wantUser string
		wantHost string
		wantErr  bool
	}{
		{name: "user and host", dest: "admin@10.0.1.1", wantUser: "admin", wantHost: "10.0.1.1"},
		{name: "host only defaults to current user", dest: "example.com", wantUser: currentName, wantHost: "example.com"},
		{name: "empty user defaults to current user", dest: "@example.com", wantUser: currentName, wantHost: "example.com"},
		{name: "user with dash", dest: "deploy-bot@example.com", wantUser: "deploy-bot", wantHost: "example.com"},
		{name: "last @ wins", dest: "a@b@host", wantUser: "a@b", wantHost: "host"},
		{name: "empty host is an error", dest: "admin@", wantErr: true},
	}

	for _, tt := range tests {
		u, h, err := parseDestination(tt.dest)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s: parseDestination(%q) expected error", tt.name, tt.dest)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: parseDestination(%q) unexpected error: %v", tt.name, tt.dest, err)
			continue
		}
		if u != tt.wantUser || h != tt.wantHost {
			t.Errorf("%s: parseDestination(%q) = (%q, %q), want (%q, %q)", tt.name, tt.dest, u, h, tt.wantUser, tt.wantHost)
		}
	}
}

func TestApplySSHConfigExplicitBatchOptionsWin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host prod\n  HostName configured.example\n  Port 2201\n  IdentityFile ~/.ssh/configured\n"), 0600); err != nil {
		t.Fatal(err)
	}
	host, _, port, key, _ := applySSHConfig("prod", "deploy", 2022, "/tmp/explicit", true, true, true)
	if host != "configured.example" || port != 2022 || key != "/tmp/explicit" {
		t.Fatalf("explicit options were overridden: host=%q port=%d key=%q", host, port, key)
	}
}

func TestSplitRemoteSpec(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantSession string
		wantPath    string
		wantOK      bool
	}{
		{name: "session and path", input: "prod:/var/log", wantSession: "prod", wantPath: "/var/log", wantOK: true},
		{name: "empty path means home", input: "prod:", wantSession: "prod", wantPath: ".", wantOK: true},
		{name: "relative remote path", input: "dev:build/out", wantSession: "dev", wantPath: "build/out", wantOK: true},
		{name: "plain local path", input: "/tmp/file.txt", wantOK: false},
		{name: "relative local path", input: "./dist/app.zip", wantOK: false},
		{name: "drive letter with backslash is local", input: `C:\data`, wantOK: false},
		{name: "single-letter session is remote", input: "a:/dst", wantSession: "a", wantPath: "/dst", wantOK: true},
		{name: "colon at start is local", input: ":foo", wantOK: false},
		{name: "slash in prefix is local", input: "a/b:c", wantOK: false},
		{name: "no colon is local", input: "justafile", wantOK: false},
	}

	for _, tt := range tests {
		sess, path, ok := splitRemoteSpec(tt.input)
		if ok != tt.wantOK || sess != tt.wantSession || path != tt.wantPath {
			t.Errorf("%s: splitRemoteSpec(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.name, tt.input, sess, path, ok, tt.wantSession, tt.wantPath, tt.wantOK)
		}
	}
}

func TestParseForwardSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantA   int
		wantB   int
		wantErr bool
	}{
		{name: "simple", spec: "8080:80", wantA: 8080, wantB: 80},
		{name: "same port", spec: "22:22", wantA: 22, wantB: 22},
		{name: "missing part", spec: "8080:", wantErr: true},
		{name: "not numeric", spec: "abc:80", wantErr: true},
		{name: "three parts", spec: "1:2:3", wantErr: true},
		{name: "zero port", spec: "0:80", wantErr: true},
		{name: "port too large", spec: "70000:80", wantErr: true},
		{name: "empty", spec: "", wantErr: true},
	}

	for _, tt := range tests {
		a, b, err := parseForwardSpec(tt.spec)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s: parseForwardSpec(%q) expected error", tt.name, tt.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: parseForwardSpec(%q) unexpected error: %v", tt.name, tt.spec, err)
			continue
		}
		if a != tt.wantA || b != tt.wantB {
			t.Errorf("%s: parseForwardSpec(%q) = (%d, %d), want (%d, %d)", tt.name, tt.spec, a, b, tt.wantA, tt.wantB)
		}
	}
}

func TestValidateCLIValues(t *testing.T) {
	for _, port := range []int{1, 22, 65535} {
		if err := validatePort(port); err != nil {
			t.Errorf("validatePort(%d) unexpected error: %v", port, err)
		}
	}
	for _, port := range []int{0, -1, 65536} {
		if err := validatePort(port); err == nil {
			t.Errorf("validatePort(%d) expected error", port)
		}
	}
	for _, timeout := range []int{0, 1, 3600} {
		if err := validateTimeout(timeout); err != nil {
			t.Errorf("validateTimeout(%d) unexpected error: %v", timeout, err)
		}
	}
	if err := validateTimeout(-1); err == nil {
		t.Error("validateTimeout(-1) expected error")
	}
}

func TestShortID(t *testing.T) {
	if got := shortID("abc"); got != "abc" {
		t.Fatalf("shortID short value = %q", got)
	}
	if got := shortID("123456789"); got != "12345678" {
		t.Fatalf("shortID long value = %q", got)
	}
}
