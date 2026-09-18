// Command icloud-resident is the resident headed Chromium holding the
// iCloud session: one browser process against the persisted profile, with
// CDP on loopback for the tools to attach to.
//
// The process is the session. A restart does not cost the login, but the
// next app-page load after one costs the owner an approval tap, which is
// why the resident loads only icloud.com at startup and opens no app tab.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

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
	logFile := filepath.Join(cfg.StateDir, "chrome.log")
	cdpPort := cfg.CDPPort()
	args := []string{
		"--enable-logging", "--log-file=" + logFile, "--log-level=1",
		"--disable-dev-shm-usage",
		"--disable-gpu",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-translate",
		"--no-first-run",
		fmt.Sprintf("--remote-debugging-port=%d", cdpPort),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profile,
		"--window-size=1280,900",
		"https://www.icloud.com/",
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot start Chromium:", err)
		return 1
	}
	cdp := browser.NewCDP(cfg.CDP)
	// Wait for CDP to answer before the liveness loop.
	for i := 0; i < 60; i++ {
		if err := cdp.Ping(); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return 0
		case <-time.After(2 * time.Second):
		}
	}
	fmt.Printf("resident browser up, CDP on %s\n", cfg.CDP)
	lastJar := time.Now()
	deadChecks := 0
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Shutting down, saving jar.
			if cookies, err := cdp.AllCookies(); err == nil {
				browser.SaveJar(state, cookies)
			}
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
			return 0
		case <-ticker.C:
			// A dead renderer takes CDP-adjacent calls down with it and
			// reads as a browser-wide wedge: close crashed tabs, not the
			// process.
			for _, id := range browser.CrashVictims(cdp, 500) {
				_ = cdp.CloseTarget(id)
				fmt.Printf("closed crashed tab %s\n", id)
			}
			if err := cdp.Ping(); err != nil {
				deadChecks++
				fmt.Printf("browser did not answer (%v), %d of 4\n", err, deadChecks)
				if deadChecks >= 4 {
					fmt.Println("browser is gone, exiting so the failure is visible")
					_ = cmd.Process.Kill()
					return 1
				}
				continue
			}
			deadChecks = 0
			if time.Since(lastJar) > 5*time.Minute {
				if cookies, err := cdp.AllCookies(); err == nil {
					if n := browser.SaveJar(state, cookies); n > 0 {
						fmt.Printf("jar: %d cookies\n", n)
					}
				}
				lastJar = time.Now()
			}
		}
	}
}
