package main

import (
	"os/exec"
	"runtime"
	"strings"
)

// gameRunning reports whether a Java process is running this instance's game, found by
// its game directory on the command line.
func gameRunning(gameDir string) bool {
	var out []byte
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
			"Get-CimInstance Win32_Process -Filter \"Name like 'java%'\" | ForEach-Object { $_.CommandLine }")
		hideWindow(cmd)
		out, _ = cmd.Output()
	} else {
		out, _ = exec.Command("ps", "-axo", "command").Output()
	}
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "\\", "/")) }
	return strings.Contains(norm(string(out)), norm(gameDir))
}
