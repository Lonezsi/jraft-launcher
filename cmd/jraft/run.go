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
	selfUpdate()

	dataDir, err := dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	openLogFile(filepath.Join(dataDir, "launcher.log"))
	if os.Getenv("JRAFT_DIR") == "" { // tests stay out of the real Start menu
		install(dataDir)
	}

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
	checkForeignMods(gameDir, lock)
	if err := applyResourcePacks(gameDir, lock); err != nil {
		return err
	}
	memMB := tune(dataDir, gameDir)
	java, err := ensureJava(dataDir, lock.Minecraft)
	if err != nil {
		return fmt.Errorf("installing Java: %w", err)
	}
	if err := writeInstance(prismData, instDir, lock, memMB, java); err != nil {
		return fmt.Errorf("setting up the instance: %w", err)
	}
	if prism == "" {
		prism, err = ensurePrism(filepath.Join(dataDir, "prism"))
		if err != nil {
			return fmt.Errorf("installing Prism Launcher: %w", err)
		}
		disablePrismUpdater(prismData)
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
	// With a server in the pack, the game joins it straight away through a relay that
	// this launcher runs for as long as the game is open.
	var tun *tunnel
	server := ""
	if lock.Server != nil && lock.Server.Address != "" {
		if tun, err = startTunnel(lock.Server.Address); err != nil {
			return fmt.Errorf("starting the connection to the server: %w", err)
		}
		server = fmt.Sprintf("127.0.0.1:%d", tun.gamePort)
		if err := addServerEntry(gameDir, tun.gamePort); err != nil {
			logf("Couldn't add JRAFT to the server list: %v", err)
		}
	}
	logf("Starting Minecraft...")
	if err := launch(prism, prismData, account, server); err != nil {
		return err
	}
	if tun == nil {
		return nil
	}
	logf("Playing on %s. Keep this window open while you play (you can minimize it).", lock.Server.Name)
	waitForGame(gameDir)
	logf("Minecraft closed.")
	return nil
}

// waitForGame returns once the game for this instance has started and then closed, or
// if it never appears within ten minutes (a launch that failed, or a sign-in left open).
func waitForGame(gameDir string) {
	seen := false
	start := time.Now()
	for {
		running := gameRunning(gameDir)
		if running {
			seen = true
		} else if seen || time.Since(start) > 10*time.Minute {
			return
		}
		time.Sleep(5 * time.Second)
	}
}
