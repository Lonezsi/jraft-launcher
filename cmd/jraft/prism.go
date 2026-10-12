package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Lonezsi/jraft-launcher/internal/pack"
)

const instanceID = "jraft"

//go:embed instance-icon.png
var instanceIcon []byte

// prismAsset is the portable Prism Launcher build for this OS, by release tag.
func prismAsset(tag string) (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "PrismLauncher-Windows-MSVC-Portable-" + tag + ".zip", nil
	case "windows/arm64":
		return "PrismLauncher-Windows-MSVC-arm64-Portable-" + tag + ".zip", nil
	case "linux/amd64":
		return "PrismLauncher-Linux-Qt6-Portable-" + tag + ".tar.gz", nil
	case "linux/arm64":
		return "PrismLauncher-Linux-aarch64-Qt6-Portable-" + tag + ".tar.gz", nil
	case "darwin/amd64", "darwin/arm64":
		return "PrismLauncher-macOS-" + tag + ".zip", nil
	}
	return "", fmt.Errorf("no Prism Launcher build for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func prismExe(dir string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(dir, "prismlauncher.exe")
	case "darwin":
		return filepath.Join(dir, "Prism Launcher.app", "Contents", "MacOS", "prismlauncher")
	default:
		return filepath.Join(dir, "PrismLauncher")
	}
}

// ensurePrism installs or updates the portable Prism Launcher in dir and
// returns the path of its executable. If GitHub can't be reached, an
// already installed copy is used as is.
func ensurePrism(dir string) (string, error) {
	current := filepath.Join(dir, "current")
	exe := prismExe(current)
	installed, _ := os.ReadFile(filepath.Join(dir, "version.txt"))

	resp, err := get("https://api.github.com/repos/PrismLauncher/PrismLauncher/releases/latest")
	if err != nil {
		if exists(exe) {
			return exe, nil
		}
		return "", err
	}
	var rel struct {
		Tag string `json:"tag_name"`
	}
	err = json.NewDecoder(resp.Body).Decode(&rel)
	resp.Body.Close()
	if err != nil || rel.Tag == "" {
		if exists(exe) {
			return exe, nil
		}
		return "", fmt.Errorf("couldn't read Prism Launcher release info")
	}
	if string(installed) == rel.Tag && exists(exe) {
		return exe, nil
	}

	asset, err := prismAsset(rel.Tag)
	if err != nil {
		return "", err
	}
	logf("Downloading Prism Launcher %s...", rel.Tag)
	archive := filepath.Join(dir, asset)
	url := "https://github.com/PrismLauncher/PrismLauncher/releases/download/" + rel.Tag + "/" + asset
	if err := downloadProgress(url, archive, "", setProgress); err != nil {
		return "", err
	}
	defer os.Remove(archive)

	staging := filepath.Join(dir, "staging")
	logf("Unpacking Prism Launcher...")
	setProgress(0, 0)
	os.RemoveAll(staging)
	if strings.HasSuffix(asset, ".zip") {
		err = unzip(archive, staging)
	} else {
		err = untargz(archive, staging)
	}
	if err != nil {
		return "", err
	}
	os.RemoveAll(current)
	if err := os.Rename(staging, current); err != nil {
		return "", err
	}
	if !exists(exe) {
		return "", fmt.Errorf("Prism Launcher archive didn't contain %s", exe)
	}
	return exe, os.WriteFile(filepath.Join(dir, "version.txt"), []byte(rel.Tag), 0o644)
}

// within joins name onto dir and refuses entries that would escape it.
func within(dir, name string) (string, error) {
	p := filepath.Join(dir, filepath.FromSlash(name))
	if p != dir && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("bad archive entry %q", name)
	}
	return p, nil
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		p, err := within(dest, f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode.IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			target, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			if err := os.Symlink(string(target), p); err != nil {
				return err
			}
			continue
		}
		err = writeFile(p, rc, mode.Perm()|0o600)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untargz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p, err := within(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(p, 0o755)
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(p), 0o755)
			err = os.Symlink(h.Linkname, p)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(p), 0o755)
			err = writeFile(p, tr, os.FileMode(h.Mode).Perm()|0o600)
		}
		if err != nil {
			return err
		}
	}
}

func writeFile(p string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// findPrism looks for a Prism Launcher the player already uses. It returns
// its executable (empty if only the data folder was found, in which case our
// portable copy is pointed at it) and its data folder (empty if none).
func findPrism() (exe, data string) {
	if os.Getenv("JRAFT_DIR") != "" { // test mode: stay isolated
		return "", ""
	}
	home, _ := os.UserHomeDir()
	var exes, datas []string
	switch runtime.GOOS {
	case "windows":
		exes = []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "PrismLauncher", "prismlauncher.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "PrismLauncher", "prismlauncher.exe"),
			filepath.Join(home, "scoop", "apps", "prismlauncher", "current", "prismlauncher.exe"),
		}
		datas = []string{filepath.Join(os.Getenv("APPDATA"), "PrismLauncher")}
	case "darwin":
		exes = []string{
			"/Applications/Prism Launcher.app/Contents/MacOS/prismlauncher",
			filepath.Join(home, "Applications", "Prism Launcher.app", "Contents", "MacOS", "prismlauncher"),
		}
		datas = []string{filepath.Join(home, "Library", "Application Support", "PrismLauncher")}
	default:
		if p, err := exec.LookPath("prismlauncher"); err == nil {
			exes = append(exes, p)
		}
		xdg := os.Getenv("XDG_DATA_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".local", "share")
		}
		datas = []string{filepath.Join(xdg, "PrismLauncher")}
		flatpakData := filepath.Join(home, ".var", "app", "org.prismlauncher.PrismLauncher", "data", "PrismLauncher")
		if exists(flatpakData) && len(exes) == 0 {
			if p, err := exec.LookPath("flatpak"); err == nil {
				// Flatpak Prism can only see its own sandboxed data folder.
				return p + "\x00run\x00org.prismlauncher.PrismLauncher", flatpakData
			}
		}
	}
	for _, d := range datas {
		if exists(filepath.Join(d, "prismlauncher.cfg")) {
			data = d
			break
		}
	}
	for _, e := range exes {
		if exists(e) {
			exe = e
			break
		}
	}
	if data == "" {
		return "", "" // an installed Prism that was never opened: use our own setup
	}
	return exe, data
}

// instancesDir respects a custom instance folder set in Prism's settings.
func instancesDir(prismData string) string {
	b, _ := os.ReadFile(filepath.Join(prismData, "prismlauncher.cfg"))
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if v, ok := strings.CutPrefix(l, "InstanceDir="); ok && v != "" {
			if filepath.IsAbs(v) {
				return v
			}
			return filepath.Join(prismData, v)
		}
	}
	return filepath.Join(prismData, "instances")
}

// writeInstance creates or updates the JRAFT instance in Prism's data folder.
func writeInstance(prismData, instDir string, lock pack.Lock, memMB int) error {
	if err := os.MkdirAll(filepath.Join(instDir, "minecraft"), 0o755); err != nil {
		return err
	}
	// Prism's own settings: answer what the first-run wizard would ask
	// (language, theme, Java) so it goes straight to the game, and let Prism download the right Java by itself.
	err := mergeCfg(filepath.Join(prismData, "prismlauncher.cfg"), map[string]string{
		"ApplicationTheme":                    "system",
		"IconTheme":                           "pe_colored",
		"Language":                            "en_US",
		"AutomaticJavaDownload":               "true",
		"AutomaticJavaSwitch":                 "true",
		"UserAskedAboutAutomaticJavaDownload": "true",
	}, false)
	if err != nil {
		return err
	}
	// Our icon for the instance in Prism's list (and its taskbar entry).
	if err := os.MkdirAll(filepath.Join(prismData, "icons"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(prismData, "icons", "jraft.png"), instanceIcon, 0o644); err != nil {
		return err
	}
	err = mergeCfg(filepath.Join(instDir, "instance.cfg"), map[string]string{
		// If Prism had to show a sign-in window, close it once the game is
		// running and quit Prism when the game exits, so only Minecraft stays.
		"OverrideMiscellaneous": "true",
		"CloseAfterLaunch":      "true",
		"QuitAfterGameStop":     "true",
		"InstanceType":          "OneSix",
		"name":                  lock.Name,
		"iconKey":               "jraft",
		"OverrideMemory":        "true",
		"MinMemAlloc":           "1024",
		"MaxMemAlloc":           strconv.Itoa(memMB),
		"OverrideJavaArgs":      "true",
		"JvmArgs":               stripMemoryArgs(lock.JavaArgs),
	}, true)
	if err != nil {
		return err
	}
	mmc := map[string]any{
		"formatVersion": 1,
		"components": []map[string]any{
			{"uid": "net.minecraft", "version": lock.Minecraft, "important": true},
			{"uid": "net.fabricmc.intermediary", "version": lock.Minecraft, "dependencyOnly": true},
			{"uid": "net.fabricmc.fabric-loader", "version": lock.LoaderVersion},
		},
	}
	b, _ := json.MarshalIndent(mmc, "", "  ")
	return os.WriteFile(filepath.Join(instDir, "mmc-pack.json"), b, 0o644)
}

// mergeCfg sets keys in a Prism key=value config file, keeping every other
// line. With overwrite false, keys that already exist are left alone.
func mergeCfg(path string, kv map[string]string, overwrite bool) error {
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
	}
	seen := map[string]bool{}
	for i, l := range lines {
		k, _, ok := strings.Cut(l, "=")
		if v, want := kv[k]; ok && want {
			seen[k] = true
			if overwrite {
				lines[i] = k + "=" + v
			}
		}
	}
	for k, v := range kv {
		if !seen[k] && v != "" {
			lines = append(lines, k+"="+v)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func launch(exe, prismData, account string) error {
	args := []string{"--dir", prismData, "--launch", instanceID}
	if account != "" {
		args = append(args, "--profile", account)
	}
	parts := strings.Split(exe, "\x00") // flatpak: "flatpak\x00run\x00<app id>"
	cmd := exec.Command(parts[0], append(parts[1:], args...)...)
	cmd.Dir = filepath.Dir(parts[0])
	return cmd.Start()
}

// disablePrismUpdater turns off Prism's own "update available" prompt for
// our portable copy. ensurePrism already keeps that copy on the latest
// release every time the app runs, so the player never sees Prism's updater.
func disablePrismUpdater(prismData string) {
	if runtime.GOOS == "darwin" {
		// The Mac build uses Sparkle, which keeps its settings in user defaults.
		exec.Command("defaults", "write", "org.prismlauncher.PrismLauncher", "SUEnableAutomaticChecks", "-bool", "false").Run()
		return
	}
	os.WriteFile(filepath.Join(prismData, "prismlauncher_update.cfg"), []byte("[General]\nauto_check=false\n"), 0o644)
}

// stripMemoryArgs drops -Xmx/-Xms: Prism sets memory itself (MaxMemAlloc)
// and warns on every launch if they also appear in the JVM arguments.
func stripMemoryArgs(args string) string {
	var keep []string
	for _, a := range strings.Fields(args) {
		if !strings.HasPrefix(a, "-Xmx") && !strings.HasPrefix(a, "-Xms") {
			keep = append(keep, a)
		}
	}
	return strings.Join(keep, " ")
}
