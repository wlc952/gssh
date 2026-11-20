//go:build windows

package exec

import "os/exec"

func configureProcessGroup(*exec.Cmd) {}
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
