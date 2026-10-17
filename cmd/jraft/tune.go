package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Hardware detection and per-PC graphics presets. The first time the pack is
// set up on a PC (and again if its graphics card changes), the launcher picks
// a preset and writes the matching video, shader and Distant Horizons
// settings. After that the player's own changes are kept.

type hardware struct {
	GPUs  []string
	VRAM  float64 // GB, best GPU, 0 if unknown
	RAM   float64 // GB
	Cores int
}

func detectHardware() hardware {
	hw := hardware{Cores: runtime.NumCPU()}
	switch runtime.GOOS {
	case "windows":
		ps := exec.Command("powershell.exe", "-NoProfile", "-Command", `
$ErrorActionPreference = 'SilentlyContinue'
Get-CimInstance Win32_VideoController | ForEach-Object { 'GPU=' + $_.Name }
Get-ItemProperty 'HKLM:\SYSTEM\ControlSet001\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}\0*' |
  Where-Object { $_.'HardwareInformation.qwMemorySize' } | ForEach-Object { 'VRAM=' + $_.'HardwareInformation.qwMemorySize' }
'RAM=' + (Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory`)
		hideWindow(ps)
		out, _ := ps.Output()
		for _, l := range strings.Split(string(out), "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
			switch k {
			case "GPU":
				hw.GPUs = append(hw.GPUs, v)
			case "VRAM":
				if n, err := strconv.ParseFloat(v, 64); err == nil && n/(1<<30) > hw.VRAM {
					hw.VRAM = n / (1 << 30)
				}
			case "RAM":
				if n, err := strconv.ParseFloat(v, 64); err == nil {
					hw.RAM = n / (1 << 30)
				}
			}
		}
	case "darwin":
		out, _ := exec.Command("system_profiler", "SPDisplaysDataType").Output()
		for _, l := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "Chipset Model:"); ok {
				hw.GPUs = append(hw.GPUs, strings.TrimSpace(v))
			}
		}
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			if n, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
				hw.RAM = n / (1 << 30)
			}
		}
	default:
		if out, err := exec.Command("lspci").Output(); err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				if strings.Contains(l, "VGA") || strings.Contains(l, "3D controller") || strings.Contains(l, "Display controller") {
					if _, name, ok := strings.Cut(l, ": "); ok {
						hw.GPUs = append(hw.GPUs, name)
					}
				}
			}
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			var kb float64
			fmt.Sscanf(strings.TrimPrefix(strings.SplitN(string(b), "\n", 2)[0], "MemTotal:"), "%f", &kb)
			hw.RAM = kb / (1 << 20)
		}
	}
	return hw
}

// Tiers, from weakest to strongest.
const (
	tierLow    = "low"    // integrated graphics and old/entry cards: few chunks, LODs, no shaders
	tierMedium = "medium" // decent cards: LODs, shaders at reduced settings
	tierHigh   = "high"   // gaming cards: shaders and LODs
	tierUltra  = "ultra"  // high-end cards: everything up
)

var gpuRules = []struct {
	tier string
	re   *regexp.Regexp
}{
	{tierUltra, regexp.MustCompile(`(?i)rtx\s*(30[89]0|40[789]0|50[6789]0)|rx\s*(7[89]00|9070)|\bM[1-5]\s*(Max|Ultra)\b`)},
	{tierHigh, regexp.MustCompile(`(?i)rtx\s*\d{4}|rx\s*(6[6-9]\d0|7[6-9]00|9060)|\barc\s*(a7[5-9]0|b5[78]0)|\bM[1-5]\s*Pro\b|radeon pro w`)},
	{tierMedium, regexp.MustCompile(`(?i)gtx\s*(10[5-8]0|16[56]0)|rx\s*(4[78]0|5[5-9]0|5[5-7]00|6[45]00)|\barc\s*a[35]\d0|radeon\s*(680|780|880|890)m|\bApple M[1-5]\b|iris\s*xe.*(max)|vega\s*(56|64)`)},
}

func gpuTier(name string) string {
	for _, r := range gpuRules {
		if r.re.MatchString(name) {
			return r.tier
		}
	}
	return tierLow
}

func pickTier(hw hardware) (tier, gpu string) {
	rank := map[string]int{tierLow: 0, tierMedium: 1, tierHigh: 2, tierUltra: 3}
	tier = tierLow
	for _, g := range hw.GPUs {
		if t := gpuTier(g); gpu == "" || rank[t] > rank[tier] {
			tier, gpu = t, g
		}
	}
	// Even a good card struggles with little system memory.
	if hw.RAM > 0 && hw.RAM < 7 && rank[tier] > rank[tierMedium] {
		tier = tierMedium
	}
	return tier, gpu
}

// memoryMB is how much RAM to give Minecraft.
func memoryMB(hw hardware) int {
	switch {
	case hw.RAM == 0:
		return 4096
	case hw.RAM < 7:
		return 2560
	case hw.RAM < 12:
		return 3072
	case hw.RAM < 24:
		return 4096
	default:
		return 6144
	}
}

type preset struct {
	options   map[string]string // options.txt
	shaders   bool
	dh        bool
	dhRadius  int
	dhQuality string
	// dhConfig is a whole DistantHorizons.toml shipped in pack/overrides/presets,
	// copied in as is. Used instead of setting single values when one known-good
	// config is better than a recipe.
	dhConfig string
	// shaderOptions, when set, picks the plain Complementary pack and writes these
	// values into its options file, so a preset decides the shader's quality too.
	shaderOptions map[string]string
}

// complementaryLowest is Complementary's own POTATO profile, pushed further where the
// shader allows it: no shadows, no SSAO, no reflections, no extra detail. The medium
// preset uses it, so mid-range cards get shaders at a frame rate they can hold.
var complementaryLowest = map[string]string{
	"SHADOW_QUALITY": "-1", "shadowDistance": "64.0", "ENTITY_SHADOW": "-1", "PLAYER_SHADOW": "-1",
	"WATER_REFLECT_QUALITY": "-1", "BLOCK_REFLECT_QUALITY": "0", "WORLD_SPACE_REFLECTIONS": "-1",
	"LIGHTSHAFT_QUALI_DEFINE": "0", "SSAO_QUALI_DEFINE": "0", "FXAA_DEFINE": "-1",
	"DETAIL_QUALITY": "0", "CLOUD_QUALITY": "1", "ANISOTROPIC_FILTER": "0",
	"COLORED_LIGHTING": "0", "RAIN_PUDDLES": "0", "SHADOW_SMOOTHING": "1",
}

var presets = map[string]preset{
	tierLow: {
		options: map[string]string{
			"renderDistance": "3", "simulationDistance": "3", "graphicsPreset": `"custom"`,
			"cutoutLeaves": "false", "improvedTransparency": "false", "biomeBlendRadius": "0",
			"entityShadows": "false", "particles": "1", "renderClouds": `"false"`,
			"weatherRadius": "5", "chunkSectionFadeInTime": "0", "entityDistanceScaling": "0.75",
			"mipmapLevels": "2", "enableVsync": "false", "maxFps": "60",
		},
		// Distant Horizons on even here: a few real chunks plus cheap LODs draw faster
		// than more real chunks, and look much further. Its config is the one that
		// worked on the maintainer's weak test laptop, copied whole.
		dh: true, dhConfig: "presets/low/DistantHorizons.toml",
	},
	tierMedium: {
		options: map[string]string{
			"renderDistance": "8", "simulationDistance": "6", "graphicsPreset": `"custom"`,
			"cutoutLeaves": "true", "improvedTransparency": "false", "biomeBlendRadius": "1",
			"entityShadows": "true", "particles": "1", "renderClouds": `"fast"`,
			"weatherRadius": "8", "chunkSectionFadeInTime": "0.5", "entityDistanceScaling": "1.0",
			"mipmapLevels": "4", "maxFps": "120",
		},
		dh: true, dhRadius: 64, dhQuality: "LOW",
		shaders: true, shaderOptions: complementaryLowest,
	},
	tierHigh: {
		options: map[string]string{
			"renderDistance": "8", "simulationDistance": "8", "biomeBlendRadius": "2",
			"particles": "0", "maxFps": "144",
		},
		shaders: true, dh: true, dhRadius: 128, dhQuality: "MEDIUM",
	},
	tierUltra: {
		options: map[string]string{
			"renderDistance": "12", "simulationDistance": "10", "biomeBlendRadius": "3",
			"particles": "0", "maxFps": "260",
		},
		shaders: true, dh: true, dhRadius: 256, dhQuality: "HIGH",
	},
}

const presetRevision = "6"

// tune applies the preset for this PC if it hasn't been applied yet (or the
// graphics card changed). It returns the memory to give Minecraft.
func tune(dataDir, gameDir string) int {
	hw := detectHardware()
	tier, gpu := pickTier(hw)
	p := loadPrefs(dataDir)
	if p.Preset != "" { // set by hand in settings.json
		tier = p.Preset
	}
	logf("PC: %s, %.0f GB RAM, %d threads -> %s preset", orUnknown(gpu), hw.RAM, hw.Cores, tier)
	// The revision is part of the key so that a change to the presets themselves reaches
	// PCs that were already tuned. Raised to 2 when the presets started choosing Distant
	// Horizons' generator: a laptop copy of the configs had left SURFACE_ONLY in place.
	// Raised to 3 when they started choosing its thread count, which the same copy had
	// pinned at the laptop's 1: one thread on Tectonic and Terralith terrain builds far
	// terrain so slowly that it looked like none was being built at all. Raised to 4
	// when the low preset switched Distant Horizons on with 3 real chunks instead of 6,
	// and to 5 when it took a whole known-good DH config and lowest-quality shaders.
	// Raised to 6 when shaders moved from the low preset to the medium one.
	key := presetRevision + "|" + tier + "|" + gpu
	applied := false
	if p.Tuned != key {
		applyPreset(gameDir, presets[tier], hw.Cores, tier)
		p.Tuned = key
		savePrefs(dataDir, p)
		logf("Applied %s graphics settings for this PC", tier)
		applied = true
	}
	if presets[tier].shaders {
		chooseShader(dataDir, gameDir, applied, presets[tier].shaderOptions)
	}
	applyAlways(gameDir)
	return memoryMB(hw)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown GPU"
	}
	return s
}

func applyPreset(gameDir string, p preset, cores int, tier string) {
	cfg := filepath.Join(gameDir, "config")
	setOptions(filepath.Join(gameDir, "options.txt"), ":", p.options)
	setOptions(filepath.Join(cfg, "iris.properties"), "=", map[string]string{"enableShaders": strconv.FormatBool(p.shaders)})
	if p.shaders && p.shaderOptions != nil {
		if pack := plainComplementary(gameDir); pack != "" {
			setOptions(filepath.Join(cfg, "iris.properties"), "=", map[string]string{"shaderPack": pack})
			setOptions(filepath.Join(gameDir, "shaderpacks", pack+".txt"), "=", p.shaderOptions)
		}
	}
	if p.dhConfig != "" {
		if b, err := os.ReadFile(filepath.Join(gameDir, filepath.FromSlash(p.dhConfig))); err == nil {
			os.WriteFile(filepath.Join(cfg, "DistantHorizons.toml"), b, 0o644)
		}
	}

	renderer := "DISABLED"
	if p.dh {
		renderer = "DEFAULT"
	}
	dh := map[string]string{"rendererMode": `"` + renderer + `"`}
	// Only on/off, far range and threads. Quality, SSAO and the generator plan are
	// left to Distant Horizons, which picks them for the hardware: on a test laptop DH
	// stopped working with the preset's values and worked again on its own defaults.
	if p.dhRadius > 0 {
		dh["lodChunkRenderDistanceRadius"] = strconv.Itoa(p.dhRadius)
		// Far terrain is built on these threads, and modded world generation is slow
		// per chunk, so a stronger PC gets more of them. A third of the CPU leaves the
		// game itself room on a mid-range machine; high and up get half.
		threads := cores / 3
		if tier == tierHigh || tier == tierUltra {
			threads = cores / 2
		}
		if threads < 2 {
			threads = 2
		}
		// A weak PC builds its far terrain slowly either way; one thread on a small
		// CPU keeps that work from taking cores the game itself needs.
		if tier == tierLow {
			threads = 1
			if cores > 4 {
				threads = 2
			}
		}
		dh["numberOfThreads"] = strconv.Itoa(threads)
		// Pausing while moving quickly is what a weak PC wants. On a strong one it
		// means flying out to look at the horizon stops the horizon being built.
		dh["pauseIfMovingQuickly"] = strconv.FormatBool(!(tier == tierHigh || tier == tierUltra))
	}
	setTOML(filepath.Join(cfg, "DistantHorizons.toml"), dh, map[string]string{
		"rendererMode":                 "client.advanced.debugging",
		"lodChunkRenderDistanceRadius": "client.advanced.graphics.quality",
		"horizontalQuality":            "client.advanced.graphics.quality",
		"verticalQuality":              "client.advanced.graphics.quality",
		"enableSsao":                   "client.advanced.graphics",
		"generatorPlan":                "common.worldGenerator",
		"numberOfThreads":              "common.multiThreading",
		"pauseIfMovingQuickly":         "common.worldGenerator",
	})
}

// fixDHGenerator undoes one combination that is never wanted: Distant Horizons drawing
// LODs with its generator plan set to DISABLED. Nothing far away is ever generated then,
// and SeedGen reads that same plan, so it stops too. It turned up on a test laptop,
// probably from the in-game menu, and looked exactly like "DH doesn't work with our
// world generation".
func fixDHGenerator(gameDir string) {
	path := filepath.Join(gameDir, "config", "DistantHorizons.toml")
	lines := readLines(path)
	renderOn, genOff := true, false
	for _, l := range lines {
		t := strings.ReplaceAll(strings.TrimSpace(l), " ", "")
		if t == `rendererMode="DISABLED"` {
			renderOn = false
		}
		if t == `generatorPlan="DISABLED"` {
			genOff = true
		}
	}
	if renderOn && genOff {
		setTOML(path, map[string]string{"generatorPlan": `"SURFACE_THEN_CHUNKS"`}, nil)
		logf("Distant Horizons was set to draw far terrain but never generate it; turned generation back on")
	}
}

// plainComplementary finds the Complementary zip the pack ships, not Euphoria
// Patches' heavier copy of it.
func plainComplementary(gameDir string) string {
	entries, _ := os.ReadDir(filepath.Join(gameDir, "shaderpacks"))
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "ComplementaryReimagined") && strings.HasSuffix(n, ".zip") && !strings.Contains(n, "Euphoria") {
			return n
		}
	}
	return ""
}

// applyAlways enforces settings every run: no Distant Horizons self-updates
// (the launcher manages mod versions) and no first-time chat messages.
func applyAlways(gameDir string) {
	fixDHGenerator(gameDir)
	cfg := filepath.Join(gameDir, "config")
	setTOML(filepath.Join(cfg, "DistantHorizons.toml"), map[string]string{
		"enableAutoUpdater":                                    "false",
		"showHighVanillaRenderDistanceWarning":                 "false",
		"generationProgressDisableMessageDisplayTimeInSeconds": "0",
	}, map[string]string{
		"enableAutoUpdater":                                    "client.advanced.autoUpdater",
		"showHighVanillaRenderDistanceWarning":                 "common.logging.warning",
		"generationProgressDisableMessageDisplayTimeInSeconds": "common.worldGenerator",
	})
	setJSONFields(filepath.Join(cfg, "terralith.json"), map[string]string{"intro_message": "false"})
}

var kvLine = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.]+)(\s*[:=]\s*)(.*)$`)

// setOptions sets key<sep>value lines, adding missing keys at the end.
func setOptions(path, sep string, kv map[string]string) {
	lines := readLines(path)
	seen := map[string]bool{}
	for i, l := range lines {
		if m := kvLine.FindStringSubmatch(l); m != nil {
			if v, ok := kv[m[2]]; ok {
				lines[i] = m[1] + m[2] + m[3] + v
				seen[m[2]] = true
			}
		}
	}
	for k, v := range kv {
		if !seen[k] {
			lines = append(lines, k+sep+v)
		}
	}
	writeLines(path, lines)
}

// setTOML sets keys in a TOML file. In an existing file only keys that are
// already there are changed (adding a second copy of a [section] would break
// the file). A missing file is created with just these keys, grouped by
// section (sections maps key -> section header); the mod fills in the rest.
func setTOML(path string, kv, sections map[string]string) {
	lines := readLines(path)
	if lines == nil {
		bySection := map[string][]string{}
		var order []string
		for k, v := range kv {
			sec := sections[k]
			if _, ok := bySection[sec]; !ok {
				order = append(order, sec)
			}
			bySection[sec] = append(bySection[sec], "	"+k+" = "+v)
		}
		for _, sec := range order {
			lines = append(lines, "["+sec+"]")
			lines = append(lines, bySection[sec]...)
		}
		writeLines(path, lines)
		return
	}
	for i, l := range lines {
		if m := kvLine.FindStringSubmatch(l); m != nil && strings.Contains(m[3], "=") {
			if v, ok := kv[m[2]]; ok {
				lines[i] = m[1] + m[2] + " = " + v
			}
		}
	}
	writeLines(path, lines)
}

// setJSONFields replaces "key": value pairs in a JSON/JSON5 file in place,
// keeping comments and formatting. Missing files and keys are left alone;
// the mod writes its defaults on first start.
func setJSONFields(path string, kv map[string]string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s := string(b)
	for k, v := range kv {
		re := regexp.MustCompile(`("` + regexp.QuoteMeta(k) + `"\s*:\s*)([^,\n}]+)`)
		s = re.ReplaceAllString(s, "${1}"+v)
	}
	os.WriteFile(path, []byte(s), 0o644)
}

func readLines(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
}

func writeLines(path string, lines []string) {
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
