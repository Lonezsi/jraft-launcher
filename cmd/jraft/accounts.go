package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// prefs is our own small settings file, next to Prism's data.
type prefs struct {
	Account string `json:"account,omitempty"` // remembered "don't ask again" choice
	// Preset forces a graphics preset (low, medium, high, ultra) instead of
	// the one picked from the hardware. Tuned records which preset was last
	// applied, so it's only applied again when that changes.
	Preset string `json:"preset,omitempty"`
	Tuned  string `json:"tuned,omitempty"`
	// Shortcuts is set once the desktop and menu shortcuts have been made.
	Shortcuts bool `json:"shortcuts,omitempty"`
}

func loadPrefs(dataDir string) prefs {
	var p prefs
	if b, err := os.ReadFile(filepath.Join(dataDir, "settings.json")); err == nil {
		json.Unmarshal(b, &p)
	}
	return p
}

func savePrefs(dataDir string, p prefs) error {
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(filepath.Join(dataDir, "settings.json"), b, 0o644)
}

// prismAccounts returns the Minecraft names of the accounts signed in to Prism.
func prismAccounts(prismData string) []string {
	b, err := os.ReadFile(filepath.Join(prismData, "accounts.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Accounts []struct {
			Profile struct {
				Name string `json:"name"`
			} `json:"profile"`
		} `json:"accounts"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	var names []string
	for _, a := range doc.Accounts {
		if a.Profile.Name != "" {
			names = append(names, a.Profile.Name)
		}
	}
	return names
}

// chooseAccount decides which account to launch with. An empty result means
// "let Prism decide", which is what happens when nobody is signed in yet:
// Prism then shows only its Microsoft sign-in window.
func chooseAccount(dataDir, prismData string, forceAsk bool) (string, error) {
	names := prismAccounts(prismData)
	p := loadPrefs(dataDir)

	switch len(names) {
	case 0:
		logf("Sign in with your Microsoft account in the window that opens. You only need to do this once.")
		return "", nil
	case 1:
		return names[0], nil
	}

	if !forceAsk && p.Account != "" {
		for _, n := range names {
			if n == p.Account {
				return n, nil
			}
		}
	}
	name, remember := askAccount(names, p.Account)
	if remember {
		p.Account = name
	} else {
		p.Account = ""
	}
	return name, savePrefs(dataDir, p)
}
