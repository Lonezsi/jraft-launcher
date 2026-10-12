package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// prefs is our own small settings file, next to Prism's data.
type prefs struct {
	Account string `json:"account,omitempty"` // remembered "don't ask again" choice
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
// Prism then shows its own Microsoft sign-in window.
func chooseAccount(dataDir, prismData string, forceAsk bool) (string, error) {
	names := prismAccounts(prismData)
	p := loadPrefs(dataDir)

	switch {
	case len(names) == 0:
		showInfo("Sign in to Minecraft",
			"First time here! Prism Launcher will open and ask you to sign in.\n\n"+
				"Click \"Add Microsoft\" and follow the steps in your browser. "+
				"The game starts once you're signed in, and you won't be asked again.")
		return "", nil
	case len(names) == 1:
		return names[0], nil
	}

	if !forceAsk && p.Account != "" {
		for _, n := range names {
			if n == p.Account {
				return n, nil
			}
		}
	}
	name, remember, ok := pickAccount(names, p.Account)
	if !ok {
		return "", fmt.Errorf("no account chosen")
	}
	if remember {
		p.Account = name
	} else {
		p.Account = ""
	}
	return name, savePrefs(dataDir, p)
}

// --- dialogs -----------------------------------------------------------------

func showInfo(title, msg string) {
	var err error
	switch runtime.GOOS {
	case "windows":
		err = runPS(`Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.MessageBox]::Show($env:JRAFT_MSG, $env:JRAFT_TITLE) | Out-Null`, map[string]string{"JRAFT_MSG": msg, "JRAFT_TITLE": title}, nil)
	case "darwin":
		err = exec.Command("osascript", "-e",
			fmt.Sprintf(`display dialog %s with title %s buttons {"OK"} default button "OK"`, asStr(msg), asStr(title))).Run()
	default:
		if p, e := exec.LookPath("zenity"); e == nil {
			err = exec.Command(p, "--info", "--title", title, "--text", msg).Run()
		} else if p, e := exec.LookPath("kdialog"); e == nil {
			err = exec.Command(p, "--title", title, "--msgbox", msg).Run()
		} else {
			err = fmt.Errorf("no dialog tool")
		}
	}
	if err != nil {
		fmt.Println("\n" + title + ": " + msg)
		waitForEnter()
	}
}

// pickAccount shows a dropdown of accounts with a "Don't ask again" checkbox.
func pickAccount(names []string, preselect string) (name string, remember, ok bool) {
	if preselect == "" {
		preselect = names[0]
	}
	switch runtime.GOOS {
	case "windows":
		if n, r, err := pickWindows(names, preselect); err == nil {
			return n, r, n != ""
		}
	case "darwin":
		if n, r, err := pickMac(names, preselect); err == nil {
			return n, r, n != ""
		}
	default:
		if n, r, err := pickLinux(names, preselect); err == nil {
			return n, r, n != ""
		}
	}
	return pickConsole(names)
}

func pickWindows(names []string, preselect string) (string, bool, error) {
	return pickWindowsScript(pickScript, names, preselect)
}

const pickScript = `Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles()
$f = New-Object System.Windows.Forms.Form
$f.Text = 'JRAFT'; $f.FormBorderStyle = 'FixedDialog'; $f.MaximizeBox = $false; $f.MinimizeBox = $false
$f.StartPosition = 'CenterScreen'; $f.ClientSize = New-Object System.Drawing.Size(320, 150); $f.TopMost = $true
$l = New-Object System.Windows.Forms.Label; $l.Text = 'Which account do you want to play on?'
$l.SetBounds(16, 14, 290, 20); $f.Controls.Add($l)
$c = New-Object System.Windows.Forms.ComboBox; $c.DropDownStyle = 'DropDownList'; $c.SetBounds(16, 40, 288, 24)
foreach ($n in ($env:JRAFT_NAMES -split "` + "`n" + `")) { [void]$c.Items.Add($n) }
$c.SelectedItem = $env:JRAFT_PRESELECT; if ($c.SelectedIndex -lt 0) { $c.SelectedIndex = 0 }
$f.Controls.Add($c)
$k = New-Object System.Windows.Forms.CheckBox; $k.Text = "Don't ask again"; $k.SetBounds(16, 74, 200, 22); $f.Controls.Add($k)
$b = New-Object System.Windows.Forms.Button; $b.Text = 'Play'; $b.SetBounds(214, 108, 90, 28)
$b.DialogResult = [System.Windows.Forms.DialogResult]::OK; $f.AcceptButton = $b; $f.Controls.Add($b)
if ($f.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { Write-Output ($c.SelectedItem + "|" + $k.Checked) }`

func pickWindowsScript(script string, names []string, preselect string) (string, bool, error) {
	var out strings.Builder
	err := runPS(script, map[string]string{
		"JRAFT_NAMES":     strings.Join(names, "\n"),
		"JRAFT_PRESELECT": preselect,
	}, &out)
	if err != nil {
		return "", false, err
	}
	name, rem, _ := strings.Cut(strings.TrimSpace(out.String()), "|")
	return name, rem == "True", nil
}

func runPS(script string, env map[string]string, out *strings.Builder) error {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-STA", "-Command", script)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if out != nil {
		cmd.Stdout = out
	}
	return cmd.Run()
}

// asStr quotes a string for AppleScript.
func asStr(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func pickMac(names []string, preselect string) (string, bool, error) {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = asStr(n)
	}
	out, err := exec.Command("osascript", "-e", fmt.Sprintf(
		`choose from list {%s} with title "JRAFT" with prompt "Which account do you want to play on?" default items {%s} OK button name "Play"`,
		strings.Join(quoted, ", "), asStr(preselect))).Output()
	if err != nil {
		return "", false, err
	}
	name := strings.TrimSpace(string(out))
	if name == "" || name == "false" {
		return "", false, nil
	}
	out, err = exec.Command("osascript", "-e", fmt.Sprintf(
		`button returned of (display dialog "Always play on %s?" with title "JRAFT" buttons {"Ask every time", "Don't ask again"} default button "Don't ask again")`,
		strings.ReplaceAll(asStr(name), `"`, ""))).Output()
	return name, err == nil && strings.TrimSpace(string(out)) == "Don't ask again", nil
}

func pickLinux(names []string, preselect string) (string, bool, error) {
	if p, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--list", "--title", "JRAFT", "--text", "Which account do you want to play on?", "--column", "Account", "--ok-label", "Play"}
		out, err := exec.Command(p, append(args, names...)...).Output()
		name := strings.TrimSpace(string(out))
		if err != nil || name == "" {
			return "", false, err
		}
		remember := exec.Command(p, "--question", "--title", "JRAFT", "--text", "Always play on "+name+"?",
			"--ok-label", "Don't ask again", "--cancel-label", "Ask every time").Run() == nil
		return name, remember, nil
	}
	if p, err := exec.LookPath("kdialog"); err == nil {
		args := append([]string{"--title", "JRAFT", "--combobox", "Which account do you want to play on?"}, names...)
		out, err := exec.Command(p, append(args, "--default", preselect)...).Output()
		name := strings.TrimSpace(string(out))
		if err != nil || name == "" {
			return "", false, err
		}
		remember := exec.Command(p, "--title", "JRAFT", "--yesno", "Always play on "+name+"?",
			"--yes-label", "Don't ask again", "--no-label", "Ask every time").Run() == nil
		return name, remember, nil
	}
	return "", false, fmt.Errorf("no dialog tool")
}

func pickConsole(names []string) (string, bool, bool) {
	fmt.Println("\nWhich account do you want to play on?")
	for i, n := range names {
		fmt.Printf("  %d) %s\n", i+1, n)
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Print("Number: ")
	line, _ := in.ReadString('\n')
	i, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || i < 1 || i > len(names) {
		return "", false, false
	}
	fmt.Print("Don't ask again? (y/N): ")
	line, _ = in.ReadString('\n')
	return names[i-1], strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y"), true
}
