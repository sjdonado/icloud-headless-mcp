// Command icloud-login runs the interactive first-login session
// bootstrap: a headed Chromium on the persisted profile, waiting up to 20
// minutes for the owner to sign in through VNC and complete 2FA. Tick
// "Trust this browser" when Apple offers it, or the session dies on the
// next relaunch.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

const loginTimeout = 20 * time.Minute

func main() {
	os.Exit(run())
}

func run() int {
	cfg := config.LoadEnv()
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	bin, err := chromiumBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	profile := filepath.Join(cfg.StateDir, "profile")
	args := []string{
		"--disable-dev-shm-usage",
		"--disable-gpu",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-translate",
		"--no-first-run",
		"--remote-debugging-port=9222",
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profile,
		"--window-size=1280,900",
		"https://www.icloud.com/",
	}
	// Deviation from production, verify-rig shaped: the login browser also
	// serves loopback CDP, so this command can poll for auth cookies and
	// the tools (and session_check) can observe the login. Same loopback
	// exposure as the resident.
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot start Chromium:", err)
		return 1
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	fmt.Println("Browser up on the virtual display. Connect over VNC and sign in.")
	fmt.Println("Tick 'Trust this browser' when Apple offers it. This exits on its own.")
	cdp := browser.NewCDP(cfg.CDP)
	deadline := time.Now().Add(loginTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		cookies, err := cdp.AllCookies()
		if err != nil {
			continue
		}
		found := 0
		for _, ck := range cookies {
			if name, _ := ck["name"].(string); browser.AuthCookieNames[name] {
				found++
			}
		}
		if found >= 2 {
			fmt.Printf("AUTHENTICATED, %d auth cookies present\n", found)
			time.Sleep(15 * time.Second) // let Apple finish writing cookies
			if cookies, err := cdp.AllCookies(); err == nil {
				if n := browser.SaveJar(state, cookies); n > 0 {
					fmt.Printf("saved %d cookies to the jar\n", n)
				}
			}
			return 0
		}
	}
	fmt.Println("TIMED OUT waiting for sign-in")
	if cookies, err := cdp.AllCookies(); err == nil {
		browser.SaveJar(state, cookies)
	}
	return 1
}

func chromiumBinary() (string, error) {
	if bin := os.Getenv("CHROMIUM_BIN"); bin != "" {
		return bin, nil
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	matches, _ := filepath.Glob(os.Getenv("PLAYWRIGHT_BROWSERS_PATH") + "/chromium-*/chrome-linux/chrome")
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("no Chromium found: set CHROMIUM_BIN")
}
