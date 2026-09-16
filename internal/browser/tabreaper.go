package browser

import (
	"fmt"
	"strings"
	"time"
)

// ReapApps are the apps that get their own warm tab, in fixed order. The
// bare icloud.com page is left alone: it is the anchor the session was
// established on, and it is idle rather than growing.
var ReapApps = []struct {
	App      string
	Fragment string
}{
	{"notes", "/notes/"},
	{"reminders", "/reminders/"},
}

// Reap closes app tabs idle past the threshold and returns the lines to
// print, in order. Closing a tab never touches the session: that lives in
// the browser process and the profile, and the next call simply pays one
// cold load. An unreachable browser is not an error: it may be deliberately
// down.
func (s State) Reap(cdp *CDP, idleMinutes int, now time.Time) []string {
	targets, err := cdp.Targets()
	if err != nil {
		return []string{"browser not reachable"}
	}
	var lines []string
	var closed []string
	for _, app := range ReapApps {
		last := s.LastUsed(app.App)
		idleFor := now.Sub(last).Minutes()
		// No marker means nothing has claimed the tab since the reaper was
		// installed: idle by definition.
		if !last.IsZero() && idleFor < float64(idleMinutes) {
			continue
		}
		for _, tab := range targets {
			if tab.Type != "page" || !strings.Contains(tab.URL, app.Fragment) {
				continue
			}
			if cerr := cdp.CloseTarget(tab.ID); cerr != nil {
				lines = append(lines, fmt.Sprintf("could not close %s: %v", app.App, cerr))
				continue
			}
			closed = append(closed, fmt.Sprintf("%s (idle %.0fm)", app.App, idleFor))
		}
	}
	if len(closed) > 0 {
		lines = append(lines, "closed: "+strings.Join(closed, ", "))
	} else {
		lines = append(lines, "nothing idle enough to close")
	}
	return lines
}
