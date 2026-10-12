package main

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Lonezsi/jraft-launcher/internal/pack"
)

// Mods the pack did not put there are left alone by the sync, on purpose: a friend may
// have added one. That also left behind every jar of an older pack that used the same
// instance folder, and a 1.21 mod next to its 26.3 replacement is a pile of
// "incompatible mod" errors at startup. So anything in mods/ that is not ours is looked
// at, and moved to mods-disabled/ if it cannot work here. Nothing is deleted.

type modInfo struct {
	ID        string
	Minecraft []string // the "minecraft" dependency, as written
	Fabric    bool     // has fabric.mod.json at all
}

func readModInfo(path string) (modInfo, error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return modInfo{}, err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != "fabric.mod.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return modInfo{}, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return modInfo{}, err
		}
		var meta struct {
			ID      string                     `json:"id"`
			Depends map[string]json.RawMessage `json:"depends"`
		}
		// Some mods put stray control characters in their metadata; Fabric copes,
		// and so should this, so only the fields we need are read.
		json.Unmarshal(b, &meta)
		info := modInfo{ID: meta.ID, Fabric: true}
		if raw, ok := meta.Depends["minecraft"]; ok {
			var one string
			var many []string
			if json.Unmarshal(raw, &one) == nil {
				info.Minecraft = []string{one}
			} else if json.Unmarshal(raw, &many) == nil {
				info.Minecraft = many
			}
		}
		return info, nil
	}
	return modInfo{}, nil
}

var versionNumber = regexp.MustCompile(`\d+(\.\d+)+`)

// fitsMinecraft is deliberately rough: a dependency that names no version, or names one
// in the same year-release line as the pack (26.x for 26.3), is taken as fitting; one
// that only names other versions (1.21.x and so on) is not. Fabric's range syntax is
// richer than this, but the point is catching jars from a different era, which always
// name a different version.
func fitsMinecraft(deps []string, mc string) bool {
	if len(deps) == 0 {
		return true
	}
	line := strings.SplitN(mc, ".", 2)[0] + "."
	for _, d := range deps {
		vs := versionNumber.FindAllString(d, -1)
		if strings.TrimSpace(d) == "*" || len(vs) == 0 {
			return true
		}
		for _, v := range vs {
			if strings.HasPrefix(v, line) || v == strings.TrimSuffix(line, ".") {
				return true
			}
		}
	}
	return false
}

// checkForeignMods moves jars that are not part of the pack and cannot work with it out
// of mods/. It runs after the sync, so the pack's own jars are all in place to compare
// mod ids against.
func checkForeignMods(gameDir string, lock pack.Lock) {
	modsDir := filepath.Join(gameDir, "mods")
	ours := map[string]bool{}
	ourIDs := map[string]bool{}
	for _, f := range lock.Files {
		if strings.HasPrefix(f.Path, "mods/") {
			name := strings.TrimPrefix(f.Path, "mods/")
			ours[name] = true
			if info, err := readModInfo(filepath.Join(modsDir, name)); err == nil && info.ID != "" {
				ourIDs[info.ID] = true
			}
		}
	}
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return
	}
	disabled := filepath.Join(gameDir, "mods-disabled")
	moved := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".jar") || ours[name] {
			continue
		}
		path := filepath.Join(modsDir, name)
		info, err := readModInfo(path)
		why := ""
		switch {
		case err != nil:
			why = "is not a readable mod file"
		case !info.Fabric:
			why = "is not a Fabric mod"
		case ourIDs[info.ID]:
			why = "is another copy of a mod the pack already has"
		case !fitsMinecraft(info.Minecraft, lock.Minecraft):
			why = "was made for Minecraft " + strings.Join(info.Minecraft, ", ") + ", not " + lock.Minecraft
		}
		if why == "" {
			continue
		}
		os.MkdirAll(disabled, 0o755)
		if os.Rename(path, filepath.Join(disabled, name)) == nil {
			logf("Moved %s to mods-disabled: it %s", name, why)
			moved++
		}
	}
	if moved > 0 {
		logf("Moved %d mod(s) that don't fit this pack out of the way. They're in %s", moved, disabled)
	}
}
