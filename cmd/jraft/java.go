package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The launcher brings its own Java, because Prism's choice cannot be trusted to be the
// right one: a player's existing Prism is often pinned to an older Java (21 for the
// 1.21 era), and Minecraft 26.x refuses to start on anything below 25. Downloading a
// runtime into our own folder and pointing only the JRAFT instance at it fixes that
// without touching the player's other instances.

// javaMajor asks Prism's metadata which Java this Minecraft version wants, so a future
// version bump in pack.json picks the right runtime without a launcher release.
func javaMajor(mc string) int {
	resp, err := get("https://meta.prismlauncher.org/v1/net.minecraft/" + mc + ".json")
	if err != nil {
		return 25
	}
	defer resp.Body.Close()
	var meta struct {
		Majors []int `json:"compatibleJavaMajors"`
	}
	if json.NewDecoder(resp.Body).Decode(&meta) != nil || len(meta.Majors) == 0 {
		return 25
	}
	best := 0
	for _, m := range meta.Majors {
		if m > best {
			best = m
		}
	}
	return best
}

// ensureJava installs (or updates) an Adoptium JRE for this Minecraft version under
// dataDir/java/<major> and returns the path of its java executable. If the download
// site cannot be reached, an already installed runtime is used as is.
func ensureJava(dataDir, mc string) (string, error) {
	major := javaMajor(mc)
	dir := filepath.Join(dataDir, "java", fmt.Sprint(major))
	current := filepath.Join(dir, "current")
	installed, _ := os.ReadFile(filepath.Join(dir, "version.txt"))
	existing := findJava(current)

	osName, arch := map[string]string{"windows": "windows", "darwin": "mac", "linux": "linux"}[runtime.GOOS],
		map[string]string{"amd64": "x64", "arm64": "aarch64"}[runtime.GOARCH]
	if osName == "" || arch == "" {
		return "", fmt.Errorf("no Java download for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	resp, err := get(fmt.Sprintf("https://api.adoptium.net/v3/assets/latest/%d/hotspot?os=%s&architecture=%s&image_type=jre", major, osName, arch))
	if err != nil {
		if existing != "" {
			return existing, nil
		}
		return "", err
	}
	var assets []struct {
		Binary struct {
			Package struct {
				Name     string `json:"name"`
				Link     string `json:"link"`
				Checksum string `json:"checksum"`
			} `json:"package"`
		} `json:"binary"`
	}
	err = json.NewDecoder(resp.Body).Decode(&assets)
	resp.Body.Close()
	if err != nil || len(assets) == 0 {
		if existing != "" {
			return existing, nil
		}
		return "", fmt.Errorf("no Java %d runtime listed for %s/%s", major, osName, arch)
	}
	pkg := assets[0].Binary.Package
	if string(installed) == pkg.Name && existing != "" {
		return existing, nil
	}

	logf("Downloading Java %d...", major)
	archive := filepath.Join(dir, pkg.Name)
	if err := downloadProgress(pkg.Link, archive, "", setProgress); err != nil {
		return "", err
	}
	defer os.Remove(archive)
	if pkg.Checksum != "" {
		if sum, err := sha256File(archive); err != nil || sum != pkg.Checksum {
			return "", fmt.Errorf("the Java download was damaged (checksum mismatch)")
		}
	}

	logf("Unpacking Java %d...", major)
	setProgress(0, 0)
	staging := filepath.Join(dir, "staging")
	os.RemoveAll(staging)
	if strings.HasSuffix(pkg.Name, ".zip") {
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
	java := findJava(current)
	if java == "" {
		return "", fmt.Errorf("the Java %d download had no java program in it", major)
	}
	return java, os.WriteFile(filepath.Join(dir, "version.txt"), []byte(pkg.Name), 0o644)
}

// findJava looks for the runtime's launcher binary inside an unpacked JRE. On
// Windows that is javaw.exe, the one that does not open a console window.
func findJava(root string) string {
	want := "java"
	if runtime.GOOS == "windows" {
		want = "javaw.exe"
	}
	var found string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !d.IsDir() && d.Name() == want && filepath.Base(filepath.Dir(p)) == "bin" {
			found = p
		}
		return nil
	})
	return found
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
