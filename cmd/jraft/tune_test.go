package main

import "testing"

func TestGPUTier(t *testing.T) {
	cases := map[string]string{
		"AMD Radeon(TM) Vega 3 Graphics":     tierLow,
		"AMD Radeon (TM) 625":                tierLow,
		"Intel(R) UHD Graphics 620":          tierLow,
		"NVIDIA GeForce MX450":               tierLow,
		"NVIDIA GeForce GTX 1060 6GB":        tierMedium,
		"NVIDIA GeForce GTX 1650":            tierMedium,
		"AMD Radeon RX 580":                  tierMedium,
		"AMD Radeon 780M Graphics":           tierMedium,
		"Apple M2":                           tierMedium,
		"NVIDIA GeForce RTX 2060":            tierHigh,
		"NVIDIA GeForce RTX 4060 Laptop GPU": tierHigh,
		"AMD Radeon RX 6700 XT":              tierHigh,
		"Apple M3 Pro":                       tierHigh,
		"NVIDIA GeForce RTX 4080 SUPER":      tierUltra,
		"AMD Radeon RX 7900 XTX":             tierUltra,
		"Apple M2 Max":                       tierUltra,
	}
	for name, want := range cases {
		if got := gpuTier(name); got != want {
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
	tier, gpu := pickTier(hardware{GPUs: []string{"Intel(R) UHD Graphics 630", "NVIDIA GeForce RTX 3070"}, RAM: 16})
	if tier != tierHigh || gpu != "NVIDIA GeForce RTX 3070" {
		t.Errorf("hybrid laptop: got %s %s", tier, gpu)
	}
}
