package main

import (
	"os/exec"
	"runtime"
	"strings"
)

// gameMarker is added to the instance's Java arguments so the game's process can be told
// apart from any other Java. Prism hands the game directory to Java on stdin, not on the
// command line, so the directory alone usually isn't there to find.
const gameMarker = "-Djraft.game=true"

// gameRunning reports whether a Java process is running this instance's game, found by
// gameMarker (or, for a game started before the marker existed, its game directory) on
// the command line.
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
	return commandLinesHaveGame(string(out), gameDir)
}

func commandLinesHaveGame(out, gameDir string) bool {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "\\", "/")) }
	return strings.Contains(norm(out), norm(gameMarker)) || strings.Contains(norm(out), norm(gameDir))
}
