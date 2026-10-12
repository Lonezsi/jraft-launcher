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
	tierLow    = "low"    // integrated graphics and old/entry cards: no shaders, no LODs
	tierMedium = "medium" // decent cards: LODs, no shaders
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
}

var presets = map[string]preset{
	tierLow: {
		options: map[string]string{
			"renderDistance": "6", "simulationDistance": "5", "graphicsPreset": `"custom"`,
			"cutoutLeaves": "false", "improvedTransparency": "false", "biomeBlendRadius": "0",
			"entityShadows": "false", "particles": "1", "renderClouds": `"false"`,
			"weatherRadius": "5", "chunkSectionFadeInTime": "0", "entityDistanceScaling": "0.75",
			"mipmapLevels": "2", "enableVsync": "false", "maxFps": "60",
		},
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
	key := tier + "|" + gpu
	if p.Tuned != key {
		applyPreset(gameDir, presets[tier])
		p.Tuned = key
		savePrefs(dataDir, p)
		logf("Applied %s graphics settings for this PC", tier)
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

func applyPreset(gameDir string, p preset) {
	cfg := filepath.Join(gameDir, "config")
	setOptions(filepath.Join(gameDir, "options.txt"), ":", p.options)
	setOptions(filepath.Join(cfg, "iris.properties"), "=", map[string]string{"enableShaders": strconv.FormatBool(p.shaders)})

	renderer := "DISABLED"
	if p.dh {
		renderer = "DEFAULT"
	}
	dh := map[string]string{"rendererMode": `"` + renderer + `"`}
	if p.dhRadius > 0 {
		dh["lodChunkRenderDistanceRadius"] = strconv.Itoa(p.dhRadius)
		dh["horizontalQuality"] = `"` + p.dhQuality + `"`
		dh["verticalQuality"] = `"` + p.dhQuality + `"`
		dh["enableSsao"] = strconv.FormatBool(p.shaders)
	}
	setTOML(filepath.Join(cfg, "DistantHorizons.toml"), dh, map[string]string{
		"rendererMode":                 "client.advanced.debugging",
		"lodChunkRenderDistanceRadius": "client.advanced.graphics.quality",
		"horizontalQuality":            "client.advanced.graphics.quality",
		"verticalQuality":              "client.advanced.graphics.quality",
		"enableSsao":                   "client.advanced.graphics",
	})
}

// applyAlways enforces settings every run: no Distant Horizons self-updates
// (the launcher manages mod versions) and no first-time chat messages.
func applyAlways(gameDir string) {
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
