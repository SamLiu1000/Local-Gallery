//go:build !windows

package mediatools

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
