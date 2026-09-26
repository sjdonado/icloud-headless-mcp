// Subcommand resident (icloud-mcp resident) is the resident headless
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
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
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
	// macOS keeps Chrome out of PATH.
	for _, path := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Chromium found: set CHROMIUM_BIN")
}

// headlessArgs runs Chromium headless, so no display is needed anywhere.
// Apple binds the session to the user agent and the headless build
// announces itself as HeadlessChrome, so it presents the ordinary Chrome
// user agent for its own version instead: the same string a headed build
// on this OS sends. ICLOUD_HEADED=1 runs it headed (a window on a desktop,
// or an X display) instead.
func headlessArgs(bin string) ([]string, error) {
	if os.Getenv("ICLOUD_HEADED") == "1" {
		return nil, nil
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return nil, fmt.Errorf("cannot read the Chromium version from %s: %v", bin, err)
	}
	ua, err := chromeUA(string(out), runtime.GOOS)
	if err != nil {
		return nil, err
	}
	return []string{"--headless=new", "--user-agent=" + ua}, nil
}

var chromeMajor = regexp.MustCompile(`(\d+)\.\d+\.\d+`)

// chromeUA is Chrome's reduced user agent: only the major version is real,
// and the platform token is frozen per OS.
func chromeUA(version, goos string) (string, error) {
	m := chromeMajor.FindStringSubmatch(version)
	if m == nil {
		return "", fmt.Errorf("no version number in %q", strings.TrimSpace(version))
	}
	platform := "X11; Linux x86_64"
	if goos == "darwin" {
		platform = "Macintosh; Intel Mac OS X 10_15_7"
	}
	return "Mozilla/5.0 (" + platform + ") AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
		m[1] + ".0.0.0 Safari/537.36", nil
}

func runResident() int {
	cfg := config.LoadEnv()
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	bin, err := chromiumBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	headless, err := headlessArgs(bin)
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
		// The resident keeps several app tabs open and drives whichever a
		// call needs; a hidden tab must not be throttled while it is driven.
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-sync",
		"--disable-translate",
		"--no-first-run",
		fmt.Sprintf("--remote-debugging-port=%d", cdpPort),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profile,
		"--window-size=1280,900",
	}
	args = append(append(args, headless...), "https://www.icloud.com/")
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
			browser.SaveJarFrom(state, cdp)
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
				if n := browser.SaveJarFrom(state, cdp); n > 0 {
					fmt.Printf("jar: %d cookies\n", n)
				}
				lastJar = time.Now()
			}
		}
	}
}

// ensureResident starts the resident browser when nothing answers on the
// configured loopback CDP port, so a laptop setup is only the MCP entry:
// the first server to start brings the browser up, detached into its own
// session so it outlives the server (and the agent) that started it.
// ICLOUD_RESIDENT=external turns it off where something else owns the
// browser, as systemd's agent-browser does on the worked-example server.
func ensureResident(cfg *config.Config) {
	if os.Getenv("ICLOUD_RESIDENT") == "external" {
		return
	}
	if u, err := url.Parse(cfg.CDP); err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		return // a remote browser is not ours to start
	}
	cdp := browser.NewCDP(cfg.CDP)
	// Two misses 3s apart, not one: a busy Chrome can miss one answer, and
	// a second resident on the same profile would hand off to the first
	// and leave two supervisors.
	answers := func() bool { _, err := cdp.DebuggerURL(); return err == nil }
	if answers() {
		return
	}
	time.Sleep(3 * time.Second)
	if answers() {
		return
	}
	// Two servers starting at once must not race two browsers onto one
	// profile.
	unlock, err := browser.AppLock(cfg.StateDir, "resident-start", 30*time.Second)
	if err != nil {
		return
	}
	defer unlock()
	if answers() {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("cannot start the resident browser: %v", err)
		return
	}
	logPath := filepath.Join(cfg.StateDir, "resident.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("cannot start the resident browser: %v", err)
		return
	}
	defer logFile.Close()
	cmd := exec.Command(exe, "resident")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		log.Printf("cannot start the resident browser: %v", err)
		return
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-exited:
			log.Printf("the resident browser exited at startup (%v); see %s", err, logPath)
			return
		case <-time.After(250 * time.Millisecond):
		}
		if answers() {
			return
		}
	}
	log.Printf("the resident browser did not answer on %s within 20s; see %s", cfg.CDP, logPath)
}
