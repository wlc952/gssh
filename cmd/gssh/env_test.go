package main

import "testing"

func TestEnvFlagsValidationAndQuoting(t *testing.T) {
	var env envFlags
	if err := env.Set("GREETING=hello world"); err != nil {
		t.Fatal(err)
	}
	if err := env.Set("EMPTY="); err != nil {
		t.Fatal(err)
	}
	if err := env.Set("1BAD=x"); err == nil {
		t.Fatal("expected invalid variable name")
	}
	if err := env.Set("MISSING"); err == nil {
		t.Fatal("expected missing equals error")
	}
	got := withEnv("printf '%s' \"$GREETING-$EMPTY\"", env)
	want := "export GREETING='hello world'; export EMPTY=''; printf '%s' \"$GREETING-$EMPTY\""
	if got != want {
		t.Fatalf("withEnv = %q, want %q", got, want)
	}
}
