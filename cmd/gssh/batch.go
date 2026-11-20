package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"gssh/internal/protocol"
)

type batchItem struct {
	Host    string               `json:"host"`
	Session string               `json:"session,omitempty"`
	Result  *protocol.ExecResult `json:"result,omitempty"`
	Error   string               `json:"error,omitempty"`
}

// readBatchHosts loads one destination per line. Blank lines and # comments
// are ignored, making the file suitable for checked-in inventory lists.
func readBatchHosts(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var hosts []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line != "" {
			hosts = append(hosts, line)
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("host file %q contains no destinations", path)
	}
	return hosts, nil
}

func handleBatch(args []string) error {
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("f", "", "Host file (one user@host destination per line)")
	parallel := fs.Int("parallel", 4, "Maximum concurrent hosts")
	port := fs.Int("p", 22, "SSH port")
	keyPath := fs.String("i", "", "SSH key path")
	password := fs.String("pswd", "", "SSH password")
	passwordStdin := fs.Bool("password-stdin", false, "Read SSH password from stdin")
	timeout := fs.Int("t", 0, "Command timeout in seconds")
	connectTimeout := fs.Int("connect-timeout", 10, "SSH connection timeout in seconds")
	jsonOut := fs.Bool("json", true, "Print one JSON result per host")
	rawOut := fs.Bool("raw", false, "Print human-readable output")
	var env envFlags
	fs.Var(&env, "e", "Set environment variable (KEY=VALUE); repeatable")
	fs.Var(&env, "env", "Set environment variable (KEY=VALUE); repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Keep track of whether -p/-i were explicitly supplied. SSH config values
	// should fill defaults, but must never override an explicit batch flag.
	portExplicit, keyExplicit := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "p":
			portExplicit = true
		case "i":
			keyExplicit = true
		}
	})
	if *file == "" {
		return fmt.Errorf("host file required: gssh batch -f hosts [flags] command")
	}
	if *parallel < 1 {
		return fmt.Errorf("--parallel must be at least 1")
	}
	if err := validatePort(*port); err != nil {
		return err
	}
	if err := validateTimeout(*timeout); err != nil {
		return err
	}
	if err := validateTimeout(*connectTimeout); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("command required")
	}
	hosts, err := readBatchHosts(*file)
	if err != nil {
		return err
	}
	resolvedPassword, err := passwordFromFlags(*password, *passwordStdin)
	if err != nil {
		return err
	}
	command := withEnv(joinCommandArgs(fs.Args()), env)

	results := make([]batchItem, len(hosts))
	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	for i, dest := range hosts {
		wg.Add(1)
		go func(i int, dest string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			item := batchItem{Host: dest}
			userName, host, e := parseDestination(dest)
			var ce error
			if e == nil {
				host, userName, batchPort, batchKey, proxyJump := applySSHConfig(host, userName, *port, *keyPath, strings.Contains(dest, "@"), portExplicit, keyExplicit)
				cp := protocol.ConnectParams{User: userName, Host: host, Port: batchPort, Password: resolvedPassword, KeyPath: batchKey, Timeout: *connectTimeout, ProxyJump: proxyJump}
				payload, connectErr := sendRequestWithTimeout(protocol.MsgConnect, cp, connectRPCDeadline(*connectTimeout))
				ce = connectErr
				if ce == nil {
					var info protocol.SessionInfo
					if ce = json.Unmarshal(payload, &info); ce == nil {
						item.Session = info.Name
						p := protocol.ExecParams{TaskID: newTaskID(), Name: info.Name, Command: command, Timeout: *timeout}
						payload, ce = sendRequestWithCmdTimeout(protocol.MsgExec, p, *timeout)
						if ce == nil {
							var r protocol.ExecResult
							ce = json.Unmarshal(payload, &r)
							item.Result = &r
						}
					}
				}
			}
			if e != nil {
				ce = e
			}
			if ce != nil {
				item.Error = ce.Error()
			}
			results[i] = item
		}(i, dest)
	}
	wg.Wait()
	failures := 0
	for _, item := range results {
		if item.Error != "" || item.Result == nil || item.Result.ExitCode != 0 {
			failures++
		}
		if *jsonOut && !*rawOut {
			b, _ := json.Marshal(item)
			fmt.Println(string(b))
		} else if item.Error != "" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", item.Host, item.Error)
		} else {
			fmt.Printf("%s: exit=%d\n%s", item.Host, item.Result.ExitCode, item.Result.Stdout)
		}
	}
	if failures > 0 {
		return fmt.Errorf("batch completed with %d/%d failures", failures, len(results))
	}
	return nil
}
