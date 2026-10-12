//go:build !windows

package main

import "os/exec"

// hideWindow has nothing to hide outside Windows.
func hideWindow(cmd *exec.Cmd) {}
