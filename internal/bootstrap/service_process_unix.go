// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package bootstrap

import (
	"os"
	"os/exec"
	"syscall"
)

func configureServiceProcess(_ *exec.Cmd) {}
func stopServiceProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer func() { _ = p.Release() }()
	return p.Signal(syscall.SIGTERM)
}
