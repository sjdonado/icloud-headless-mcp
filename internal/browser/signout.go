package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const homeURL = "https://www.icloud.com/"

// accountMenuPointJS returns the center of the home page's Account button
// (ui-button[aria-label=Account], top right) or, with item set, of the open
// account menu's entry whose text is item. Inspected live (2026-09-26):
// the menu holds the account header, "iCloud Settings", "Manage Apple
// Account" and "Sign Out" as ui-menu-item[role=menuitem]. Hidden
// duplicates are skipped, and whitespace (nbsp included) is normalised.
const accountMenuPointJS = `(item) => {
  const found = [];
  const walk = (root) => {
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot);
      found.push(el);
    }
  };
  walk(document);
  const norm = (s) => (s || '').replace(/\s+/g, ' ').trim();
  const el = found.find((e) => e.getBoundingClientRect().width > 0 && (item
    ? e.getAttribute('role') === 'menuitem' && norm(e.innerText) === item
    : e.tagName.toLowerCase() === 'ui-button' && e.getAttribute('aria-label') === 'Account'));
  if (!el) return null;
  const b = el.getBoundingClientRect();
  return {x: Math.round(b.x + b.width / 2), y: Math.round(b.y + b.height / 2)};
}`

// signedOutOrigins are the origins whose site data holds the session or
// the apps' cached data. Cookies are cleared for every site regardless:
// the profile belongs to this server alone.
var signedOutOrigins = []string{
	"https://www.icloud.com", "https://icloud.com", "https://setup.icloud.com",
	"https://idmsa.apple.com", "https://appleid.apple.com", "https://www.apple.com",
}

// SignOutResult says which steps of a sign-out completed, so a caller can
// tell a clean sign-out from a partial one and route the next step.
type SignOutResult struct {
	AppleSignOut   bool     `json:"apple_sign_out"`  // Apple's own Sign Out ended the token before the wipe
	JarRemoved     bool     `json:"jar_removed"`     // the saved cookie jar is gone
	CookiesCleared bool     `json:"cookies_cleared"` // every cookie in the profile is gone
	DataErrors     []string `json:"site_data_errors,omitempty"`
	TabsClosed     int      `json:"app_tabs_closed"`
}

// SignOut signs the resident browser out of iCloud: Apple's own Sign Out
// from the account menu when menu is set (so Apple ends the session too),
// then the local wipe: the saved jar and the latch first (so a failure
// later still leaves the next call routed to open_login), every cookie,
// the iCloud and Apple site data, and the open Notes and Reminders tabs,
// which still render decrypted content. Callers hold the app locks and
// JarLock.
func SignOut(state State, menu bool) (SignOutResult, error) {
	var res SignOutResult
	cdp := NewCDP(state.CDP)
	targets, err := cdp.Targets()
	if err != nil {
		return res, err
	}
	targetID := ""
	for _, tg := range targets {
		if tg.Type == "page" && strings.TrimSuffix(tg.URL, "/") == strings.TrimSuffix(homeURL, "/") {
			targetID = tg.ID
			break
		}
	}
	wsURL, err := cdp.DebuggerURL()
	if err != nil {
		return res, err
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return res, err
	}
	defer conn.close()

	if menu {
		res.AppleSignOut = appleSignOut(conn, targetID)
	}
	// App tabs close first: they hold decrypted content, and a live page
	// could write storage back after the wipe. First also means an error
	// later never leaves them open.
	for _, tg := range targets {
		if tg.Type == "page" && appURL(tg.URL) && cdp.CloseTarget(tg.ID) == nil {
			res.TabsClosed++
		}
	}

	jar := filepath.Join(state.Dir, "cookies.json")
	removeJar := func() bool {
		err := os.Remove(jar)
		return err == nil || os.IsNotExist(err)
	}
	res.JarRemoved = removeJar()
	// Signed out is not waiting for approval: the latch would route the
	// next failure to reask_access instead of open_login.
	state.ClearBlocked()

	if err := conn.call("", "Storage.clearCookies", map[string]any{}, nil); err != nil {
		return res, fmt.Errorf("could not clear the browser's cookies: %v", err)
	}
	// clearDataForOrigin answers "Internal error" on the browser target and
	// works on a page session (verified 2026-09-26, Chromium 153), so it
	// goes through a tab: the home tab, else any page, else a fresh one.
	pageID := targetID
	for _, tg := range targets {
		if pageID == "" && tg.Type == "page" && !appURL(tg.URL) {
			pageID = tg.ID
		}
	}
	if pageID == "" {
		if pageID, err = conn.newTab(homeURL); err != nil {
			return res, err
		}
	}
	page, err := conn.tabAttach(pageID)
	if err != nil {
		return res, err
	}
	for _, origin := range signedOutOrigins {
		if err := conn.call(page, "Storage.clearDataForOrigin", map[string]any{"origin": origin, "storageTypes": "all"}, nil); err != nil {
			res.DataErrors = append(res.DataErrors, origin+": "+err.Error())
		}
	}
	conn.tabDetach(page)
	cookies, err := conn.cookies()
	if err != nil {
		return res, err
	}
	res.CookiesCleared = !HasAuthCookies(cookies)
	// Again, after the wipe: the resident saves the jar every few minutes
	// and may have written it between the first remove and the clear.
	res.JarRemoved = removeJar() && res.JarRemoved
	if targetID != "" {
		if session, err := conn.tabAttach(targetID); err == nil {
			_ = conn.navigate(session, homeURL)
			conn.tabDetach(session)
		}
	}
	if !res.CookiesCleared {
		return res, fmt.Errorf("the session token is still in the browser after clearing it")
	}
	return res, nil
}

// appleSignOut clicks Account, then Sign Out, on the home tab, and reports
// true only once the session token is actually gone: a click that missed,
// or a sign-out still in flight, must not read as Apple having signed out.
func appleSignOut(conn *wsConn, targetID string) bool {
	if targetID == "" {
		return false
	}
	session, err := conn.tabAttach(targetID)
	if err != nil {
		return false
	}
	defer conn.tabDetach(session)
	_ = conn.call(session, "Page.bringToFront", map[string]any{}, nil)
	point := func(item any) *struct{ X, Y int } {
		raw, err := conn.isolatedWorld(session, targetID, "", accountMenuPointJS, item)
		if err != nil {
			return nil
		}
		var pt *struct{ X, Y int }
		_ = json.Unmarshal(raw, &pt)
		return pt
	}
	acct := point(nil)
	if acct == nil || conn.mouseClick(session, acct.X, acct.Y) != nil {
		return false
	}
	sleepMS(1500)
	item := point("Sign Out")
	if item == nil || conn.mouseClick(session, item.X, item.Y) != nil {
		return false
	}
	for i := 0; i < 20; i++ {
		sleepMS(500)
		// The session token is what ends the session; Apple may leave its
		// other web cookies behind, which the local wipe then clears.
		if cookies, err := conn.cookies(); err == nil && !HasSessionToken(cookies) {
			return true
		}
	}
	return false
}

// appURL reports a Notes or Reminders app tab.
func appURL(u string) bool {
	return strings.HasPrefix(u, "https://www.icloud.com/notes") || strings.HasPrefix(u, "https://www.icloud.com/reminders")
}
