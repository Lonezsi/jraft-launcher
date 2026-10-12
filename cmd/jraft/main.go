// jraft keeps a friend's JRAFT install in sync with this repo and starts the
// game through a bundled, portable Prism Launcher.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

// For testing: JRAFT_PACK_DIR reads lock.json and overrides from a local
// pack folder, JRAFT_DIR replaces the data folder, JRAFT_NO_LAUNCH skips starting the game.
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
	if err := run(); err != nil {
		fmt.Println("\nSomething went wrong:", err)
		fmt.Println("Send a screenshot of this window to whoever runs the pack.")
		waitForEnter()
		os.Exit(1)
	}
	time.Sleep(3 * time.Second)
}

func waitForEnter() {
	fmt.Print("\nPress Enter to close...")
	bufio.NewReader(os.Stdin).ReadString('\n')
}

func run() error {
	checkSelfUpdate()

	dataDir, err := dataDir()
	if err != nil {
		return err
	}
	prismData := filepath.Join(dataDir, "prism-data")
	instDir := filepath.Join(prismData, "instances", instanceID)
	gameDir := filepath.Join(instDir, "minecraft")
	fmt.Println("Game folder:", gameDir)

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

	if err := syncFiles(gameDir, lock); err != nil {
		return err
	}
	if err := writeDefaultOptions(gameDir, lock); err != nil {
		return err
	}
	if err := writeInstance(prismData, instDir, lock); err != nil {
		return fmt.Errorf("setting up the instance: %w", err)
	}
	prism, err := ensurePrism(filepath.Join(dataDir, "prism"))
	if err != nil {
		return fmt.Errorf("installing Prism Launcher: %w", err)
	}
	if os.Getenv("JRAFT_NO_LAUNCH") != "" {
		return nil
	}
	fmt.Println("\nStarting Minecraft...")
	fmt.Println("First time? Prism will ask you to sign in with your Microsoft account. After that it just starts.")
	return launch(prism, prismData)
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

func dataDir() (string, error) {
	if d := os.Getenv("JRAFT_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "jraft"), nil
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "jraft"), nil
	default:
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, "jraft"), nil
		}
		return filepath.Join(home, ".local", "share", "jraft"), nil
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

type state struct {
	Files map[string]string `json:"files"` // path -> sha512 we installed
}

func syncFiles(gameDir string, lock pack.Lock) error {
	statePath := filepath.Join(gameDir, ".jraft-state.json")
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
		if h, err := pack.HashFile(filepath.Join(gameDir, filepath.FromSlash(f.Path))); err == nil && h == f.SHA512 {
			continue
		}
		jobs = append(jobs, job{f, f.URL})
	}
	for _, f := range lock.Overrides {
		if err := pack.SafePath(f.Path); err != nil {
			return err
		}
		if !exists(filepath.Join(gameDir, filepath.FromSlash(f.Path))) {
			jobs = append(jobs, job{f, rawURL("overrides/" + f.Path)})
		}
	}

	// Remove files we installed earlier that are no longer in the pack.
	// Files the player added themselves are never touched.
	removed := 0
	for p := range old.Files {
		if !want[p] && pack.SafePath(p) == nil {
			if os.Remove(filepath.Join(gameDir, filepath.FromSlash(p))) == nil {
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
				err := download(j.url, filepath.Join(gameDir, filepath.FromSlash(j.f.Path)), j.f.SHA512)
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

// download fetches url to dest, checking the sha512 when one is given.
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
	if sha != "" {
		if h, err := pack.HashFile(tmp); err != nil || h != sha {
			os.Remove(tmp)
			return fmt.Errorf("checksum mismatch")
		}
	}
	os.Remove(dest)
	return os.Rename(tmp, dest)
}

// writeDefaultOptions turns on the pack's resource packs for a fresh install.
func writeDefaultOptions(gameDir string, lock pack.Lock) error {
	p := filepath.Join(gameDir, "options.txt")
	if exists(p) || len(lock.ResourcePacks) == 0 {
		return nil
	}
	packs, _ := json.Marshal(append([]string{"vanilla"}, lock.ResourcePacks...))
	return os.WriteFile(p, []byte("resourcePacks:"+string(packs)+"\n"), 0o644)
}
