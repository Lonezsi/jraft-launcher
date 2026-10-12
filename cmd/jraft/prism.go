package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Lonezsi/jraft-launcher/internal/pack"
)

const instanceID = "jraft"

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
	fmt.Println("Downloading Prism Launcher", rel.Tag+"...")
	archive := filepath.Join(dir, asset)
	url := "https://github.com/PrismLauncher/PrismLauncher/releases/download/" + rel.Tag + "/" + asset
	if err := download(url, archive, ""); err != nil {
		return "", err
	}
	defer os.Remove(archive)

	staging := filepath.Join(dir, "staging")
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

// writeInstance creates or updates the JRAFT instance in Prism's data folder.
func writeInstance(prismData, instDir string, lock pack.Lock) error {
	if err := os.MkdirAll(filepath.Join(instDir, "minecraft"), 0o755); err != nil {
		return err
	}
	// Prism's own settings: skip as much of the first-run wizard as we can
	// and let Prism download the right Java by itself.
	err := mergeCfg(filepath.Join(prismData, "prismlauncher.cfg"), map[string]string{
		"Language":                            "en_US",
		"AutomaticJavaDownload":               "true",
		"AutomaticJavaSwitch":                 "true",
		"UserAskedAboutAutomaticJavaDownload": "true",
	}, false)
	if err != nil {
		return err
	}
	err = mergeCfg(filepath.Join(instDir, "instance.cfg"), map[string]string{
		"InstanceType":     "OneSix",
		"name":             lock.Name,
		"iconKey":          "grass",
		"OverrideMemory":   "true",
		"MinMemAlloc":      "2048",
		"MaxMemAlloc":      "6144",
		"OverrideJavaArgs": "true",
		"JvmArgs":          lock.JavaArgs,
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

func launch(exe, prismData string) error {
	cmd := exec.Command(exe, "--dir", prismData, "--launch", instanceID)
	cmd.Dir = filepath.Dir(exe)
	return cmd.Start()
}
