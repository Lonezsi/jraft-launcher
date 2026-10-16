// jraft keeps a friend's JRAFT install in sync with this repo and starts the
// game through a bundled, portable Prism Launcher.
package main

import (
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
// pack folder, JRAFT_DIR replaces the data folder, JRAFT_NO_LAUNCH stops before starting the game.
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
	// Servers (GitHub, Modrinth's CDN) sometimes answer 502/503/504 or drop
	// the connection for a moment, so those get a few patient retries.
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<(attempt-1)) * time.Second) // 1, 2, 4, 8, 16s
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 200 {
			return resp, nil
		}
		resp.Body.Close()
		lastErr = fmt.Errorf("GET %s: %s", u, resp.Status)
		if resp.StatusCode < 500 && resp.StatusCode != 429 {
			break // a real "not found" or similar; retrying won't help
		}
	}
	return nil, lastErr
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
		logf("A newer launcher (%s) is available: %s", rel.Tag, rel.URL)
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
	// OverridesRevision is the pack's forced-config revision last applied here.
	OverridesRevision int `json:"overridesRevision,omitempty"`
	// Stamps remember size and modification time of each verified file,
	// so unchanged files don't have to be hashed again on every run.
	Stamps map[string]string `json:"stamps,omitempty"`
}

func stamp(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
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
		local := filepath.Join(gameDir, filepath.FromSlash(f.Path))
		if st := stamp(local); st != "" && old.Files[f.Path] == f.StateKey() && old.Stamps[f.Path] == st {
			continue
		}
		// A stripped file no longer matches its sha512, so it is only trusted by stamp.
		if h, err := pack.HashFile(local); err == nil && h == f.SHA512 && len(f.Strip) == 0 {
			continue
		}
		jobs = append(jobs, job{f, f.URL})
	}
	for _, f := range lock.Overrides {
		if err := pack.SafePath(f.Path); err != nil {
			return err
		}
		forced := false
		if old.OverridesRevision < lock.OverridesRevision {
			for _, p := range lock.ForceOverrides {
				forced = forced || p == f.Path
			}
		}
		if forced || !exists(filepath.Join(gameDir, filepath.FromSlash(f.Path))) {
			jobs = append(jobs, job{f, rawURL("overrides/" + f.Path)})
		}
	}

	// Remove files we installed earlier that are no longer in the pack.
	// Files the player added themselves are never touched.
	removed := 0
	for p := range old.Files {
		if !want[p] && pack.SafePath(p) == nil {
			if os.Remove(filepath.Join(gameDir, filepath.FromSlash(p))) == nil {
				logf("Removed %s", p)
				removed++
			}
		}
	}

	if len(jobs) == 0 && removed == 0 {
		logf("Everything is up to date.")
	} else if len(jobs) > 0 {
		logf("Downloading %d file(s)...", len(jobs))
		setProgress(0, int64(len(jobs)))
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
				dest := filepath.Join(gameDir, filepath.FromSlash(j.f.Path))
				err := download(j.url, dest, j.f.SHA512)
				if err == nil && len(j.f.Strip) > 0 {
					err = pack.StripZip(dest, j.f.Strip)
				}
				mu.Lock()
				done++
				setProgress(int64(done), int64(len(jobs)))
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", j.f.Path, err))
					logf("[%d/%d] FAILED %s", done, len(jobs), j.f.Path)
				} else {
					logf("[%d/%d] %s", done, len(jobs), j.f.Path)
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

	newState := state{Files: map[string]string{}, Stamps: map[string]string{}, OverridesRevision: old.OverridesRevision}
	if len(errs) == 0 {
		newState.OverridesRevision = lock.OverridesRevision
	}
	failed := map[string]bool{}
	for _, e := range errs {
		failed[strings.SplitN(e.Error(), ":", 2)[0]] = true
	}
	for _, f := range lock.Files {
		newState.Files[f.Path] = f.StateKey()
		if !failed[f.Path] {
			newState.Stamps[f.Path] = stamp(filepath.Join(gameDir, filepath.FromSlash(f.Path)))
		}
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
	return downloadProgress(url, dest, sha, nil)
}

// downloadProgress is download with an optional (bytes so far, total) callback.
func downloadProgress(url, dest, sha string, progress func(n, total int64)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastErr = tryDownload(url, dest, sha, progress); lastErr == nil {
			return nil
		}
		time.Sleep(time.Second * time.Duration(attempt+1))
	}
	return lastErr
}

func tryDownload(url, dest, sha string, progress func(n, total int64)) error {
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
	var body io.Reader = resp.Body
	if progress != nil {
		body = &progressReader{r: resp.Body, total: resp.ContentLength, fn: progress}
	}
	_, err = io.Copy(out, body)
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

// applyResourcePacks writes the pack's resource packs into options.txt, in the pack's
// order, whenever that list changes (and on a fresh install). Order matters: the last
// pack wins, and Grassrise only shows when nothing above it replaces the grass models,
// so the pack's last entry is kept on top. Packs the player turned on themselves are
// kept, just below that top one.
func applyResourcePacks(gameDir string, lock pack.Lock) error {
	if len(lock.ResourcePacks) == 0 {
		return nil
	}
	stampPath := filepath.Join(gameDir, ".jraft-packs")
	stamp := strings.Join(lock.ResourcePacks, "\n")
	optPath := filepath.Join(gameDir, "options.txt")
	if b, err := os.ReadFile(stampPath); err == nil && string(b) == stamp && exists(optPath) {
		return nil
	}

	lines := readLines(optPath)
	var current, incompatible []string
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "resourcePacks:"); ok {
			json.Unmarshal([]byte(v), &current)
		}
		if v, ok := strings.CutPrefix(l, "incompatibleResourcePacks:"); ok {
			json.Unmarshal([]byte(v), &incompatible)
		}
	}
	ours := map[string]bool{"vanilla": true}
	for _, p := range lock.ResourcePacks {
		ours[p] = true
	}
	// Packs from an earlier version of the pack list are dropped too, so a pack taken
	// out of the pack is also turned off. Only file/ packs are ever ours.
	previous := map[string]bool{}
	if b, err := os.ReadFile(stampPath); err == nil {
		for _, p := range strings.Split(string(b), "\n") {
			previous[p] = true
		}
	}
	var extras []string
	for _, p := range current {
		if !ours[p] && !previous[p] {
			extras = append(extras, p)
		}
	}
	top := lock.ResourcePacks[len(lock.ResourcePacks)-1]
	order := append([]string{"vanilla"}, lock.ResourcePacks[:len(lock.ResourcePacks)-1]...)
	order = append(append(order, extras...), top)

	// Packs made for an older 26.x are "incompatible" to Minecraft, which turns them
	// off at startup unless they are listed here, the list the game itself keeps for
	// packs a player said yes to.
	seenInc := map[string]bool{}
	for _, p := range incompatible {
		seenInc[p] = true
	}
	for _, p := range lock.ResourcePacks {
		if strings.HasPrefix(p, "file/") && !seenInc[p] {
			incompatible = append(incompatible, p)
		}
	}
	rp, _ := json.Marshal(order)
	ip, _ := json.Marshal(incompatible)
	setOptions(optPath, ":", map[string]string{
		"resourcePacks":             string(rp),
		"incompatibleResourcePacks": string(ip),
	})
	logf("Turned on %d resource packs, %s on top", len(lock.ResourcePacks), strings.TrimPrefix(top, "file/"))
	return os.WriteFile(stampPath, []byte(stamp), 0o644)
}

type progressReader struct {
	r        io.Reader
	n, total int64
	fn       func(n, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	p.fn(p.n, p.total)
	return n, err
}
