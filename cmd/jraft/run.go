package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Lonezsi/jraft-launcher/internal/pack"
)

func main() {
	runUI(run)
}

func run() error {
	logf("JRAFT launcher %s", version)
	checkSelfUpdate()

	dataDir, err := dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	openLogFile(filepath.Join(dataDir, "launcher.log"))

	// Use the player's own Prism Launcher (and its accounts) if they have
	// one; otherwise a portable copy is downloaded into our folder below.
	prism, prismData := findPrism()
	if prismData == "" {
		prismData = filepath.Join(dataDir, "prism-data")
	} else {
		logf("Using your Prism Launcher: %s", prismData)
	}
	instDir := filepath.Join(instancesDir(prismData), instanceID)
	gameDir := filepath.Join(instDir, "minecraft")
	logf("Game folder: %s", gameDir)

	logf("Checking for pack updates...")
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
	logf("%s for Minecraft %s, Fabric %s", lock.Name, lock.Minecraft, lock.LoaderVersion)

	if err := syncFiles(gameDir, lock); err != nil {
		return err
	}
	if err := writeDefaultOptions(gameDir, lock); err != nil {
		return err
	}
	if err := writeInstance(prismData, instDir, lock); err != nil {
		return fmt.Errorf("setting up the instance: %w", err)
	}
	if prism == "" {
		prism, err = ensurePrism(filepath.Join(dataDir, "prism"))
		if err != nil {
			return fmt.Errorf("installing Prism Launcher: %w", err)
		}
	}
	setProgress(1, 1)

	forceAsk := len(os.Args) > 1 && os.Args[1] == "--choose-account"
	if p := loadPrefs(dataDir); !forceAsk && p.Account != "" && len(prismAccounts(prismData)) > 1 {
		logf("Playing as %s", p.Account)
		forceAsk = offerSwitch(3 * time.Second)
	}
	account, err := chooseAccount(dataDir, prismData, forceAsk)
	if err != nil {
		return err
	}
	if os.Getenv("JRAFT_NO_LAUNCH") != "" {
		logf("(test mode) would launch %s as %q", prism, account)
		return nil
	}
	logf("Starting Minecraft...")
	return launch(prism, prismData, account)
}
