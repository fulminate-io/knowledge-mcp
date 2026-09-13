// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"os/exec"
	"syscall"
)

func configureServiceProcess(cmd *exec.Cmd) {
	// A session daemon has no console tied to the CLI that started it.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | syscall.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
func stopServiceProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer func() { _ = p.Release() }()
	return p.Kill()
}
