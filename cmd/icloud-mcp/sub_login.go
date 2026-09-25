// Subcommand login (icloud-mcp login) is the operator's first-login path:
// it opens the same login door open_login does, onto the running
// resident, prints the link and one-time password, and waits up to the
// door's TTL for the owner to sign in and complete 2FA. Tick "Trust this
// browser" when Apple offers it, or the session dies on the next relaunch.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/session"
)

func runLogin() int {
	cfg := config.LoadEnv()
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	cdp := browser.NewCDP(cfg.CDP)
	switch outcome, _ := cdp.Check(); outcome {
	case browser.OK:
		fmt.Println("already signed in")
		return 0
	case browser.NeedsApproval:
		// Signed in already; the grant is reask's job, never a door's.
		fmt.Println("signed in, waiting for the data-access approval: run `icloud-mcp reask` instead")
		return 0
	case browser.Unreachable:
		fmt.Fprintln(os.Stderr, "no browser answers on", cfg.CDP, "- start `icloud-mcp resident` first")
		return 2
	}
	// Ctrl-C, a kill, or a dropped SSH session closes the door now instead
	// of at its TTL. Registered before the door opens, so no window leaves
	// an open door behind.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	d, pass, closeDoor, err := session.OpenDoor(state)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeDoor()
	deadline := time.Unix(d.Expires, 0)
	fmt.Printf("Login door open until %s (%s):\n  %s\n  username: root\n  password: %s\n",
		deadline.Format("15:04"), d.Via, d.URL, pass)
	fmt.Println("Sign in, enter the 2FA code, tick 'Trust this browser'. This exits on its own.")
	for time.Now().Before(deadline) {
		select {
		case <-stop:
			fmt.Println("interrupted, door closed")
			return 1
		case <-time.After(5 * time.Second):
		}
		outcome, _ := cdp.Check()
		if outcome != browser.OK && outcome != browser.NeedsApproval {
			continue
		}
		time.Sleep(15 * time.Second) // let Apple finish writing cookies
		if cookies, err := cdp.AllCookies(); err == nil {
			if n := browser.SaveJar(state, cookies); n > 0 {
				fmt.Printf("saved %d cookies to the jar\n", n)
			}
		}
		if outcome == browser.NeedsApproval {
			fmt.Println("SIGNED IN, waiting for the data-access approval on a device")
		} else {
			fmt.Println("AUTHENTICATED")
		}
		return 0
	}
	fmt.Println("TIMED OUT waiting for sign-in")
	return 1
}
