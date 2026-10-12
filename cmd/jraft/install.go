package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// install puts the launcher somewhere permanent and gives it a shortcut, so a friend
// who ran it once from Downloads can find it again without the download.
//
// The copy is refreshed on every run when it differs from the running file, which is how
// a newly downloaded version replaces the installed one. Shortcuts are made only once:
// someone who deletes the desktop icon does not want it back on every launch.
func install(dataDir string) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	self, _ = filepath.EvalSymlinks(self)
	p := loadPrefs(dataDir)

	switch runtime.GOOS {
	case "windows":
		dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "JRAFT")
		target := filepath.Join(dir, "JRAFT.exe")
		if !strings.EqualFold(self, target) && !sameFile(self, target) {
			os.MkdirAll(dir, 0o755)
			if copyFile(self, target) == nil {
				logf("Installed the launcher to %s", target)
			}
		}
		if p.Shortcuts || !exists(target) {
			return
		}
		// WScript.Shell is the one shortcut writer every Windows has. The paths go in
		// through the environment so nothing in them needs quoting for PowerShell.
		err := runHidden("powershell.exe", []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", `
$s = New-Object -ComObject WScript.Shell
foreach ($place in @([Environment]::GetFolderPath('Desktop'), (Join-Path ([Environment]::GetFolderPath('Programs')) ''))) {
  $l = $s.CreateShortcut((Join-Path $place 'JRAFT.lnk'))
  $l.TargetPath = $env:JRAFT_TARGET
  $l.WorkingDirectory = Split-Path $env:JRAFT_TARGET
  $l.Description = 'Play JRAFT'
  $l.Save()
}`}, "JRAFT_TARGET="+target)
		if err == nil {
			logf("Added JRAFT to the desktop and the Start menu")
			p.Shortcuts = true
			savePrefs(dataDir, p)
		}

	case "linux":
		bin := filepath.Join(dataDir, "jraft")
		if self != bin && !sameFile(self, bin) {
			if copyFile(self, bin) == nil {
				os.Chmod(bin, 0o755)
			}
		}
		if p.Shortcuts {
			return
		}
		icon := filepath.Join(dataDir, "icon.png")
		os.WriteFile(icon, instanceIcon, 0o644)
		apps := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications")
		os.MkdirAll(apps, 0o755)
		desktop := "[Desktop Entry]\nType=Application\nName=JRAFT\nComment=Play JRAFT\n" +
			"Exec=\"" + bin + "\"\nIcon=" + icon + "\nTerminal=false\nCategories=Game;\n"
		if os.WriteFile(filepath.Join(apps, "jraft.desktop"), []byte(desktop), 0o755) == nil {
			logf("Added JRAFT to the applications menu")
			p.Shortcuts = true
			savePrefs(dataDir, p)
		}
	}
	// macOS: JRAFT.app is already an app; dragging it to Applications is the install.
}

func sameFile(a, b string) bool {
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	out.Close()
	os.Remove(dst)
	return os.Rename(tmp, dst)
}

func runHidden(name string, args []string, env ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	hideWindow(cmd)
	return cmd.Run()
}
