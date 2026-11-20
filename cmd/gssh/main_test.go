package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectRPCDeadlineHonorsRequestedTimeout(t *testing.T) {
	if got := connectRPCDeadline(60); got < 60*time.Second {
		t.Fatalf("deadline = %s, want at least 60s", got)
	}
	if got := connectRPCDeadline(0); got != connectRPCTimeout {
		t.Fatalf("default deadline = %s, want %s", got, connectRPCTimeout)
	}
}

func TestReadBatchHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("# inventory\nroot@a\n\n b.example # comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := readBatchHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0] != "root@a" || h[1] != "b.example" {
		t.Fatalf("hosts = %#v", h)
	}
}

func TestReadBatchHostsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("# only comments\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBatchHosts(path); err == nil {
		t.Fatal("expected empty host file error")
	}
}

func TestPasswordFromFlags(t *testing.T) {
	got, err := passwordFromFlags("secret", false)
	if err != nil || got != "secret" {
		t.Fatalf("explicit password = %q, %v", got, err)
	}
	if _, err := passwordFromFlags("secret", true); err == nil {
		t.Fatal("expected conflict when both password options are supplied")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("p a s s\r\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	got, err = passwordFromFlags("", true)
	os.Stdin = old
	r.Close()
	if err != nil || got != "p a s s" {
		t.Fatalf("stdin password = %q, %v", got, err)
	}
}

func TestJoinCommandArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "empty", args: nil, want: ""},
		{name: "single", args: []string{"echo"}, want: "echo"},
		{name: "single full command string stays raw", args: []string{"echo hello | wc -c"}, want: "echo hello | wc -c"},
		{name: "multiple args are shell quoted", args: []string{"echo", "hello", "world"}, want: "'echo' 'hello' 'world'"},
		{name: "space in argument is preserved", args: []string{"touch", "a b"}, want: "'touch' 'a b'"},
		{name: "metacharacters stay literal", args: []string{"printf", "%s\n", "*"}, want: "'printf' '%s\n' '*'"},
		{name: "single quote is escaped safely", args: []string{"printf", "pa'ss"}, want: "'printf' 'pa'\"'\"'ss'"},
		{name: "empty arg is preserved", args: []string{"printf", ""}, want: "'printf' ''"},
	}

	for _, tt := range tests {
		if got := joinCommandArgs(tt.args); got != tt.want {
			t.Fatalf("%s: joinCommandArgs() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestUsageJSONSchema(t *testing.T) {
	b, err := usageJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name     string `json:"name"`
		Commands []struct {
			Name  string `json:"name"`
			Usage string `json:"usage"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.Name != "gssh" || len(doc.Commands) < 10 {
		t.Fatalf("unexpected help document: %#v", doc)
	}
	seen := map[string]bool{}
	for _, c := range doc.Commands {
		if c.Name == "exec" {
			seen[c.Name] = c.Usage != ""
		}
	}
	if !seen["exec"] {
		t.Fatal("exec command missing from JSON help")
	}
}
