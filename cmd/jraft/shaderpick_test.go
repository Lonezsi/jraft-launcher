package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func irisSetting(t *testing.T, gameDir, key string) string {
	t.Helper()
	for _, l := range readLines(filepath.Join(gameDir, "config", "iris.properties")) {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestApplyShader(t *testing.T) {
	gameDir := t.TempDir()
	sp := filepath.Join(gameDir, "shaderpacks")
	os.MkdirAll(sp, 0o755)
	for _, f := range []string{"ComplementaryReimagined_r5.9.3.zip", "rethinking-voxels_r0.1-beta9.zip", "rethinking-voxels_r0.1-beta9.zip.txt"} {
		os.WriteFile(filepath.Join(sp, f), nil, 0o644)
	}
	low := map[string]string{"SHADOW_QUALITY": "-1"}

	if pending := applyShader(gameDir, shaderRethinking, low); pending {
		t.Error("Rethinking Voxels should never be pending")
	}
	if got := irisSetting(t, gameDir, "shaderPack"); got != "rethinking-voxels_r0.1-beta9.zip" {
		t.Errorf("shaderPack = %q", got)
	}
	if _, err := os.Stat(filepath.Join(sp, "rethinking-voxels_r0.1-beta9.zip.txt")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(sp, "rethinking-voxels_r0.1-beta9.zip.txt")); len(b) != 0 {
		t.Error("Complementary's settings were written into Rethinking Voxels' file")
	}

	// Before the first game start Euphoria hasn't built its copy yet: plain Complementary.
	if pending := applyShader(gameDir, shaderComplementary, low); !pending {
		t.Error("want pending while Euphoria's copy is missing")
	}
	if got := irisSetting(t, gameDir, "shaderPack"); got != "ComplementaryReimagined_r5.9.3.zip" {
		t.Errorf("shaderPack = %q", got)
	}

	// After it, Euphoria's copy, with the preset's settings in its own file.
	os.Mkdir(filepath.Join(sp, "ComplementaryReimagined_r5.9.3 + EuphoriaPatches_1.10.5"), 0o755)
	if pending := applyShader(gameDir, shaderComplementary, low); pending {
		t.Error("still pending with Euphoria's copy present")
	}
	if got := irisSetting(t, gameDir, "shaderPack"); got != "ComplementaryReimagined_r5.9.3 + EuphoriaPatches_1.10.5" {
		t.Errorf("shaderPack = %q", got)
	}
	if got := irisSetting(t, gameDir, "enableShaders"); got != "true" {
		t.Errorf("enableShaders = %q", got)
	}
	b, _ := os.ReadFile(filepath.Join(sp, "ComplementaryReimagined_r5.9.3 + EuphoriaPatches_1.10.5.txt"))
	if !strings.Contains(string(b), "SHADOW_QUALITY=-1") {
		t.Errorf("preset settings missing from Euphoria's settings file: %q", b)
	}
}
