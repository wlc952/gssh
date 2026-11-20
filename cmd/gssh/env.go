package main

import (
	"fmt"
	"regexp"
	"strings"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envFlags implements repeatable -e/--env options (KEY=VALUE). Values are
// exported in the command's shell so they work for both remote and local
// execution, including streamed commands.
type envFlags []string

func (e *envFlags) String() string { return strings.Join(*e, ",") }
func (e *envFlags) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("invalid environment assignment %q: expected KEY=VALUE", v)
	}
	key := v[:strings.IndexByte(v, '=')]
	if !envNamePattern.MatchString(key) {
		return fmt.Errorf("invalid environment variable name %q", key)
	}
	*e = append(*e, v)
	return nil
}

func withEnv(command string, env envFlags) string {
	if len(env) == 0 {
		return command
	}
	prefix := make([]string, 0, len(env))
	for _, assignment := range env {
		i := strings.IndexByte(assignment, '=')
		prefix = append(prefix, "export "+assignment[:i]+"="+shellQuote(assignment[i+1:])+";")
	}
	return strings.Join(prefix, " ") + " " + command
}
