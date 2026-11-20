package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out, err := captureStdout(func() error { return handleCompletion([]string{shell}) })
		if err != nil {
			t.Fatalf("%s: %v", shell, err)
		}
		if len(out) < 100 || !strings.Contains(out, "gssh") {
			t.Fatalf("%s completion output is unexpectedly short", shell)
		}
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	if err := handleCompletion([]string{"powershell"}); err == nil {
		t.Fatal("expected unsupported shell error")
	}
	if err := handleCompletion(nil); err == nil {
		t.Fatal("expected usage error")
	}
}

func TestCompletionUsesSupportedFlags(t *testing.T) {
	out, err := captureStdout(func() error { return handleCompletion([]string{"fish"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "__fish_seen_subcommand_from connect use' -s p -l port") {
		t.Fatal("fish completion advertises unsupported --port")
	}
	if strings.Contains(out, "__fish_seen_subcommand_from batch' -l stream") {
		t.Fatal("fish completion advertises unsupported batch --stream")
	}
}

func captureStdout(fn func() error) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	callErr := fn()
	_ = w.Close()
	os.Stdout = old
	var b bytes.Buffer
	_, readErr := io.Copy(&b, r)
	_ = r.Close()
	if callErr != nil {
		return b.String(), callErr
	}
	return b.String(), readErr
}
