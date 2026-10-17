package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// selfUpdate replaces this launcher with the newest release and restarts into it.
//
// Shortcuts point at the installed copy, which used to be replaced only when someone ran
// a newer download by hand. A PC that never did kept an old launcher, and an old launcher
// cannot reach the server (the relay came later), so every start now checks first.
//
// Windows cannot overwrite a running .exe, but it can rename one: the running file
// steps aside as .old, the new one takes its name, and the new one is started. The .old
// is removed by the next run. Mac builds are an .app bundle and are left to the
// download page.
func selfUpdate() {
	if version == "dev" || os.Getenv("JRAFT_NO_UPDATE") != "" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	self, _ = filepath.EvalSymlinks(self)
	os.Remove(self + ".old")

	asset := map[string]string{
		"windows/amd64": "jraft-windows.exe",
		"linux/amd64":   "jraft-linux-amd64",
		"linux/arm64":   "jraft-linux-arm64",
	}[runtime.GOOS+"/"+runtime.GOARCH]

	resp, err := get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	err = json.NewDecoder(resp.Body).Decode(&rel)
	resp.Body.Close()
	if err != nil || !newerVersion(rel.Tag, version) {
		return
	}
	if asset == "" {
		logf("A newer launcher (%s) is available: %s", rel.Tag, rel.URL)
		return
	}
	for _, a := range rel.Assets {
		if a.Name != asset {
			continue
		}
		logf("Updating the launcher to %s...", rel.Tag)
		tmp := self + ".new"
		if err := downloadProgress(a.URL, tmp, "", setProgress); err != nil {
			logf("Couldn't update the launcher: %v", err)
			return
		}
		if fi, err := os.Stat(tmp); err != nil || (a.Size > 0 && fi.Size() != a.Size) {
			os.Remove(tmp)
			logf("Couldn't update the launcher: the download was incomplete")
			return
		}
		os.Chmod(tmp, 0o755)
		if err := os.Rename(self, self+".old"); err != nil {
			os.Remove(tmp)
			logf("Couldn't update the launcher: %v", err)
			return
		}
		if err := os.Rename(tmp, self); err != nil {
			os.Rename(self+".old", self)
			logf("Couldn't update the launcher: %v", err)
			return
		}
		cmd := exec.Command(self, os.Args[1:]...)
		if cmd.Start() == nil {
			os.Exit(0)
		}
		return
	}
}

// newerVersion compares tags like v1.10.0 numerically, so v1.10.0 is newer than v1.9.0
// and a launcher ahead of the latest release is never "updated" backwards.
func newerVersion(tag, current string) bool {
	parse := func(v string) []int {
		var out []int
		for _, p := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	a, b := parse(tag), parse(current)
	if a == nil || b == nil {
		return false
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}
