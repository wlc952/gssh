package ssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveHostConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte("Host prod\n  HostName real.example\n  User deploy\n  Port 2201\n  IdentityFile ~/.ssh/prod_key\n  ProxyJump jumpuser@bastion\nHost *\n  User fallback\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := resolveHostConfigFile(p, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "deploy" || c.Port != 2201 || c.IdentityFile == "" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.ProxyJump != "jumpuser@bastion" {
		t.Fatalf("ProxyJump = %q", c.ProxyJump)
	}
}

func TestResolveHostConfigMissing(t *testing.T) {
	c, err := resolveHostConfigFile(filepath.Join(t.TempDir(), "missing"), "x")
	if err != nil || c != (HostConfig{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestResolveHostConfigNegatedPattern(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte("Host * !prod\n  User fallback\nHost prod\n  User deploy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	prod, err := resolveHostConfigFile(p, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if prod.User != "deploy" {
		t.Fatalf("negated wildcard incorrectly applied to prod: %+v", prod)
	}
	dev, err := resolveHostConfigFile(p, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if dev.User != "fallback" {
		t.Fatalf("positive wildcard not applied to dev: %+v", dev)
	}
}

func TestResolveHostConfigSpecificOverridesWildcard(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	content := "Host *\n  User wildcard\n  Port 22\nHost prod\n  User deploy\n  Port 2201\n"
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := resolveHostConfigFile(p, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "deploy" || c.Port != 2201 {
		t.Fatalf("specific stanza did not override wildcard: %+v", c)
	}
}
