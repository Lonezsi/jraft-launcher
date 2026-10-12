package main

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a helper program (PowerShell, mostly) from flashing a console window:
// the launcher itself is a window-only program, so any console it starts would be the
// only console on screen.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
