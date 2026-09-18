// Package session owns recovery: re-firing Apple's data-access prompt and
// opening the supervised VNC login door. Both run behind confirmed tools
// and both reap expired login doors on entry; the reask and login
// subcommands delegate to the same cores.
package session

import (
	"fmt"
	"os"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

// remindersURL is the one app tab re-navigation targets. The grant covers
// iCloud.com data rather than a single application, so one navigation
// raises one prompt.
const remindersURL = "https://www.icloud.com/reminders/"

// Reask re-navigates the Reminders tab and clears the blocked latch,
// exactly what the reask subcommand always did. It returns the human
// message plus the historical exit-style code: 0 re-asked, 1 navigation
// failed, 2 no timezone or no browser.
func Reask(cfg *config.Config) (string, int) {
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	zone, err := cfg.LocalTimezone()
	if err != nil {
		return fmt.Sprintf("no timezone: %v", err), 2
	}
	cdp := browser.NewCDP(cfg.CDP)
	if _, err := cdp.DebuggerURL(); err != nil {
		return fmt.Sprintf("could not reach the browser: %v", err), 2
	}
	if err := browser.Renavigate(cdp, remindersURL, zone); err != nil {
		return fmt.Sprintf("%s: %v", remindersURL, err), 1
	}
	// The reaper closes tabs idle past 15 minutes; a re-asked tab counts
	// as just used, like every other entry point promises.
	state.Touch("reminders")
	// Release the latch so the next real call can find out whether the
	// owner approved. A re-ask is the owner saying they are at a device.
	state.ClearBlocked()
	return "re-asked " + remindersURL + "\nlatch cleared, iCloud will be tried again", 0
}

// MaybeReap closes login doors only when one is recorded, so healthy
// browser calls that prove a login sweep the door without paying for a
// session check on every call.
func MaybeReap(state browser.State) {
	if _, err := os.Stat(doorRecord(state)); err != nil {
		return
	}
	ReapDoors(state)
}

// ownerNow resolves the owner's zone and current time for quiet-hours
// decisions. Helpers that must start without credentials run under
// LoadEnv, same as the reask subcommand.
func ownerNow(cfg *config.Config) (*time.Location, time.Time, error) {
	zone, err := cfg.LocalTimezone()
	if err != nil {
		return nil, time.Time{}, err
	}
	owner, err := time.LoadLocation(zone)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("unknown timezone %q", zone)
	}
	return owner, time.Now(), nil
}

// quietRefusal reports whether a fresh app-page load must be deferred to
// the morning. Renavigate navigates unconditionally (unlike App.Open,
// which refuses itself), so tool-driven re-asks need this gate, or a
// night call raises a device prompt nobody can answer.
func quietRefusal(owner *time.Location, now time.Time, app string) string {
	if !browser.QuietHoursAt(owner, now) {
		return ""
	}
	return fmt.Sprintf("iCloud %s would have to be loaded fresh, which asks Apple for "+
		"approval on your devices, and it is the middle of the night. Deferred until "+
		"morning rather than spending a prompt you cannot answer. Nothing was done.", app)
}
