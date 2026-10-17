package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lonezsi/jraft-launcher/internal/pack"
)

func writePack(t *testing.T, path, mcmeta string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	e, _ := w.Create("pack.mcmeta")
	e.Write([]byte(mcmeta))
	w.Close()
	f.Close()
}

func readPackOptions(t *testing.T, gameDir string) (rp, inc []string) {
	t.Helper()
	for _, l := range readLines(filepath.Join(gameDir, "options.txt")) {
		if v, ok := strings.CutPrefix(l, "resourcePacks:"); ok {
			json.Unmarshal([]byte(v), &rp)
		}
		if v, ok := strings.CutPrefix(l, "incompatibleResourcePacks:"); ok {
			json.Unmarshal([]byte(v), &inc)
		}
	}
	return
}

// The state the v1 launcher left on players' PCs: every pack listed as incompatible.
// Minecraft then took each compatible one off that list and left it switched off.
func setupBrokenInstall(t *testing.T) string {
	gameDir := t.TempDir()
	os.MkdirAll(filepath.Join(gameDir, "resourcepacks"), 0o755)
	writePack(t, filepath.Join(gameDir, "resourcepacks", "new.zip"), `{"pack":{"min_format":[97,1],"max_format":[97,1]}}`)
	writePack(t, filepath.Join(gameDir, "resourcepacks", "old.zip"), `{"pack":{"pack_format":88}}`)
	os.WriteFile(filepath.Join(gameDir, "options.txt"), []byte(
		`resourcePacks:["vanilla","file/mine.zip"]`+"\n"+
			`incompatibleResourcePacks:["file/new.zip","file/old.zip","file/mine.zip"]`+"\n"), 0o644)
	os.WriteFile(filepath.Join(gameDir, ".jraft-packs"), []byte("file/new.zip\nfile/old.zip"), 0o644)
	return gameDir
}

func TestResourcePacksWithNoMorePopups(t *testing.T) {
	gameDir := setupBrokenInstall(t)
	lock := pack.Lock{
		ResourceFormat: "97.1",
		ResourcePacks:  []string{"file/old.zip", "file/new.zip"},
		Files:          []pack.File{{Path: "mods/nomorepopups-fabric-26.2-2.3.1.jar"}},
	}
	if err := applyResourcePacks(gameDir, lock); err != nil {
		t.Fatal(err)
	}
	rp, inc := readPackOptions(t, gameDir)
	if strings.Join(rp, ",") != "vanilla,file/old.zip,file/mine.zip,file/new.zip" {
		t.Errorf("resourcePacks = %v", rp)
	}
	// No More Popups makes every pack compatible, so none of ours may be listed;
	// the player's own entry stays.
	if strings.Join(inc, ",") != "file/mine.zip" {
		t.Errorf("incompatibleResourcePacks = %v, want only the player's own", inc)
	}
}

func TestResourcePacksByFormat(t *testing.T) {
	gameDir := setupBrokenInstall(t)
	lock := pack.Lock{ResourceFormat: "97.1", ResourcePacks: []string{"file/old.zip", "file/new.zip"}}
	if err := applyResourcePacks(gameDir, lock); err != nil {
		t.Fatal(err)
	}
	_, inc := readPackOptions(t, gameDir)
	if strings.Join(inc, ",") != "file/old.zip,file/mine.zip" {
		t.Errorf("incompatibleResourcePacks = %v, want old.zip listed and new.zip not", inc)
	}
	// A second run with the same pack list leaves the options alone.
	os.WriteFile(filepath.Join(gameDir, "options.txt"), []byte("resourcePacks:[]\n"), 0o644)
	applyResourcePacks(gameDir, lock)
	if rp, _ := readPackOptions(t, gameDir); len(rp) != 0 {
		t.Errorf("options rewritten although the pack list did not change: %v", rp)
	}
}
