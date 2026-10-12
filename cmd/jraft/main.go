// jraft keeps a friend's JRAFT install in sync with this repo and opens the
// official Minecraft Launcher with the JRAFT profile selected.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Lonezsi/jraft-launcher/internal/pack"
)

// Set at build time with -ldflags "-X main.repo=owner/name -X main.version=v1.2.3".
var (
	repo    = "Lonezsi/jraft-launcher"
	branch  = "main"
	version = "dev"
)

const profileID = "jraft"

// For testing: JRAFT_PACK_DIR reads lock.json and overrides from a local
// pack folder, JRAFT_MC_DIR replaces .minecraft, JRAFT_NO_LAUNCH skips opening the launcher.
func rawURL(p string) string {
	if d := os.Getenv("JRAFT_PACK_DIR"); d != "" {
		return "file://" + filepath.ToSlash(filepath.Join(d, p))
	}
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/pack/%s", repo, branch, p)
}

var client = &http.Client{Timeout: 10 * time.Minute}

func get(u string) (*http.Response, error) {
	if local, ok := strings.CutPrefix(u, "file://"); ok {
		f, err := os.Open(strings.SplitN(local, "?", 2)[0])
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: f}, nil
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Lonezsi/jraft-launcher/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp, nil
}

func main() {
	fmt.Printf("JRAFT launcher %s\n\n", version)
	err := run()
	if err != nil {
		fmt.Println("\nSomething went wrong:", err)
		fmt.Println("Send a screenshot of this window to whoever runs the pack.")
		waitForEnter()
		os.Exit(1)
	}
	fmt.Println("\nAll set. In the Minecraft Launcher pick the \"JRAFT\" installation and hit Play.")
	time.Sleep(4 * time.Second)
}

func waitForEnter() {
	fmt.Print("\nPress Enter to close...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func run() error {
	checkSelfUpdate()

	mcDir, err := minecraftDir()
	if err != nil {
		return err
	}
	instDir := filepath.Join(mcDir, "jraft")
	fmt.Println("Minecraft folder:", mcDir)
	fmt.Println("Pack folder:     ", instDir)

	fmt.Println("\nChecking for pack updates...")
	resp, err := get(rawURL("lock.json") + "?t=" + fmt.Sprint(time.Now().Unix()))
	if err != nil {
		return fmt.Errorf("could not reach GitHub: %w", err)
	}
	var lock pack.Lock
	err = json.NewDecoder(resp.Body).Decode(&lock)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("bad lock.json: %w", err)
	}
	fmt.Printf("%s for Minecraft %s, Fabric %s\n", lock.Name, lock.Minecraft, lock.LoaderVersion)

	if err := installFabric(mcDir, lock); err != nil {
		return fmt.Errorf("installing Fabric: %w", err)
	}
	if err := syncFiles(instDir, lock); err != nil {
		return err
	}
	if err := writeDefaultOptions(instDir, lock); err != nil {
		return err
	}
	if err := writeProfile(mcDir, instDir, lock); err != nil {
		return fmt.Errorf("adding launcher profile: %w", err)
	}
	if os.Getenv("JRAFT_NO_LAUNCH") != "" {
		return nil
	}
	fmt.Println("\nOpening the Minecraft Launcher...")
	return openLauncher()
}

func checkSelfUpdate() {
	if version == "dev" {
		return
	}
	resp, err := get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var rel struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if json.NewDecoder(resp.Body).Decode(&rel) == nil && rel.Tag != "" && rel.Tag != version {
		fmt.Printf("A newer launcher (%s) is available: %s\n\n", rel.Tag, rel.URL)
	}
}

func minecraftDir() (string, error) {
	if d := os.Getenv("JRAFT_MC_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), ".minecraft"), nil
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "minecraft"), nil
	default:
		flatpak := filepath.Join(home, ".var", "app", "com.mojang.Minecraft", ".minecraft")
		normal := filepath.Join(home, ".minecraft")
		if !exists(normal) && exists(flatpak) {
			return flatpak, nil
		}
		return normal, nil
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// installFabric does what the Fabric installer does for the vanilla launcher:
// drops the loader's version JSON (plus an empty jar) into versions/.
func installFabric(mcDir string, lock pack.Lock) error {
	id := lock.FabricID()
	dir := filepath.Join(mcDir, "versions", id)
	jsonPath := filepath.Join(dir, id+".json")
	if exists(jsonPath) {
		return nil
	}
	fmt.Println("Installing Fabric", lock.LoaderVersion)
	resp, err := get(fmt.Sprintf("https://meta.fabricmc.net/v2/versions/loader/%s/%s/profile/json", lock.Minecraft, lock.LoaderVersion))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jar"), nil, 0o644); err != nil {
		return err
	}
	return os.WriteFile(jsonPath, data, 0o644)
}

type state struct {
	Files map[string]string `json:"files"` // path -> sha512 we installed
}

func syncFiles(instDir string, lock pack.Lock) error {
	statePath := filepath.Join(instDir, ".jraft-state.json")
	var old state
	if b, err := os.ReadFile(statePath); err == nil {
		json.Unmarshal(b, &old)
	}

	type job struct {
		f   pack.File
		url string
	}
	var jobs []job
	want := map[string]bool{}
	for _, f := range lock.Files {
		if err := pack.SafePath(f.Path); err != nil {
			return err
		}
		want[f.Path] = true
		if h, err := pack.HashFile(filepath.Join(instDir, filepath.FromSlash(f.Path))); err == nil && h == f.SHA512 {
			continue
		}
		jobs = append(jobs, job{f, f.URL})
	}
	for _, f := range lock.Overrides {
		if err := pack.SafePath(f.Path); err != nil {
			return err
		}
		if !exists(filepath.Join(instDir, filepath.FromSlash(f.Path))) {
			jobs = append(jobs, job{f, rawURL("overrides/" + f.Path)})
		}
	}

	// Remove files we installed earlier that are no longer in the pack.
	// Files the player added themselves are never touched.
	removed := 0
	for p := range old.Files {
		if !want[p] && pack.SafePath(p) == nil {
			if os.Remove(filepath.Join(instDir, filepath.FromSlash(p))) == nil {
				fmt.Println("  removed", p)
				removed++
			}
		}
	}

	if len(jobs) == 0 && removed == 0 {
		fmt.Println("Everything is up to date.")
	} else if len(jobs) > 0 {
		fmt.Printf("Downloading %d file(s)...\n", len(jobs))
	}

	var (
		mu   sync.Mutex
		errs []error
		done int
		wg   sync.WaitGroup
		ch   = make(chan job)
	)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				err := download(j.url, filepath.Join(instDir, filepath.FromSlash(j.f.Path)), j.f.SHA512)
				mu.Lock()
				done++
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", j.f.Path, err))
					fmt.Printf("  [%d/%d] FAILED %s\n", done, len(jobs), j.f.Path)
				} else {
					fmt.Printf("  [%d/%d] %s\n", done, len(jobs), j.f.Path)
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()

	newState := state{Files: map[string]string{}}
	for _, f := range lock.Files {
		newState.Files[f.Path] = f.SHA512
	}
	b, _ := json.MarshalIndent(newState, "", "  ")
	if err := os.WriteFile(statePath, b, 0o644); err != nil {
		return err
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func download(url, dest, sha string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastErr = tryDownload(url, dest, sha); lastErr == nil {
			return nil
		}
		time.Sleep(time.Second * time.Duration(attempt+1))
	}
	return lastErr
}

func tryDownload(url, dest, sha string) error {
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, resp.Body)
	out.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if h, err := pack.HashFile(tmp); err != nil || h != sha {
		os.Remove(tmp)
		return fmt.Errorf("checksum mismatch")
	}
	os.Remove(dest)
	return os.Rename(tmp, dest)
}

// writeDefaultOptions turns on the pack's resource packs for a fresh install.
func writeDefaultOptions(instDir string, lock pack.Lock) error {
	p := filepath.Join(instDir, "options.txt")
	if exists(p) || len(lock.ResourcePacks) == 0 {
		return nil
	}
	packs, _ := json.Marshal(append([]string{"vanilla"}, lock.ResourcePacks...))
	return os.WriteFile(p, []byte("resourcePacks:"+string(packs)+"\n"), 0o644)
}

// writeProfile adds or updates the JRAFT installation in the official
// launcher's profile list, keeping everything else in the file untouched.
func writeProfile(mcDir, instDir string, lock pack.Lock) error {
	files := []string{filepath.Join(mcDir, "launcher_profiles.json")}
	store := filepath.Join(mcDir, "launcher_profiles_microsoft_store.json")
	if exists(store) {
		files = append(files, store)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for _, f := range files {
		doc := map[string]any{}
		if b, err := os.ReadFile(f); err == nil {
			if err := json.Unmarshal(b, &doc); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		} else {
			doc["version"] = 3
			doc["settings"] = map[string]any{}
		}
		profiles, _ := doc["profiles"].(map[string]any)
		if profiles == nil {
			profiles = map[string]any{}
		}
		p, _ := profiles[profileID].(map[string]any)
		if p == nil {
			p = map[string]any{"created": now, "icon": "Grass", "javaArgs": lock.JavaArgs}
		}
		p["name"] = lock.Name
		p["type"] = "custom"
		p["lastVersionId"] = lock.FabricID()
		p["gameDir"] = instDir
		p["lastUsed"] = now
		profiles[profileID] = p
		doc["profiles"] = profiles

		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(f, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func openLauncher() error {
	switch runtime.GOOS {
	case "windows":
		for _, p := range []string{
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Minecraft Launcher", "MinecraftLauncher.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Minecraft Launcher", "MinecraftLauncher.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Minecraft Launcher", "MinecraftLauncher.exe"),
		} {
			if exists(p) {
				return exec.Command(p).Start()
			}
		}
		// Microsoft Store / Xbox app version.
		return exec.Command("explorer.exe", `shell:AppsFolder\Microsoft.4297127D64EC6_8wekyb3d8bbwe!Minecraft`).Start()
	case "darwin":
		return exec.Command("open", "-a", "Minecraft").Run()
	default:
		if p, err := exec.LookPath("minecraft-launcher"); err == nil {
			return exec.Command(p).Start()
		}
		if p, err := exec.LookPath("flatpak"); err == nil {
			out, _ := exec.Command(p, "info", "com.mojang.Minecraft").CombinedOutput()
			if !strings.Contains(string(out), "not installed") && len(out) > 0 {
				return exec.Command(p, "run", "com.mojang.Minecraft").Start()
			}
		}
		return errors.New("couldn't find the Minecraft Launcher. Install it from minecraft.net, then run this again (your files are already set up)")
	}
}
