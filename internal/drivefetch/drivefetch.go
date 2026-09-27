// Package drivefetch pulls the configured iCloud Drive app libraries,
// through the resident browser's session.
//
// It talks to what the Drive web app talks to rather than driving its UI:
// drivews/retrieveItemDetailsInFolders lists a folder by drivewsid, and
// docws/.../download/batch turns a file's docwsid into a signed URL whose
// bytes are the file. Both accept the browser's own cookies. A fetch from
// the page's JavaScript is refused by CORS, so the API calls go over plain
// HTTP with the cookie header, not through the page.
package drivefetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/dvlibraries"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

const driveURL = "https://www.icloud.com/iclouddrive/"

// Months returns the most recent count months as YYYY-MM, newest first.
// More than one, because samples land late in the month just ended.
func Months(count int, today time.Time) []string {
	if count < 1 {
		count = 1
	}
	var out []string
	cursor := today
	for i := 0; i < count; i++ {
		out = append(out, cursor.Format("2006-01"))
		cursor = time.Date(cursor.Year(), cursor.Month(), 1, 0, 0, 0, 0, cursor.Location()).AddDate(0, 0, -1)
	}
	return out
}

// API talks to the Drive hosts with the browser's cookies.
type API struct {
	DriveBase string // scheme+host for retrieveItemDetailsInFolders
	DriveQ    string // captured query string
	DocBase   string
	DocQ      string
	Cookies   string // Cookie header value
	Client    *http.Client
}

func (a *API) post(url, q string, body any) (any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", url+"?"+q, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return a.do(req, url)
}

func (a *API) get(url, q string) (any, error) {
	req, err := http.NewRequest("GET", url+"?"+q, nil)
	if err != nil {
		return nil, err
	}
	return a.do(req, url)
}

func (a *API) do(req *http.Request, url string) (any, error) {
	req.Header.Set("Origin", "https://www.icloud.com")
	req.Header.Set("Referer", "https://www.icloud.com/")
	req.Header.Set("Content-Type", "text/plain")
	if a.Cookies != "" {
		req.Header.Set("Cookie", a.Cookies)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, strings.Split(url, "?")[0], truncate(string(data), 200))
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Items lists a folder by drivewsid.
func (a *API) Items(drivewsid string) ([]map[string]any, error) {
	out, err := a.post(a.DriveBase+"/retrieveItemDetailsInFolders", a.DriveQ,
		[]any{map[string]any{"drivewsid": drivewsid, "partialData": false, "includeHierarchy": false}})
	if err != nil {
		return nil, err
	}
	list, ok := out.([]any)
	if !ok {
		raw, _ := json.Marshal(out)
		return nil, fmt.Errorf("unexpected drive response envelope: %s", truncate(string(raw), 200))
	}
	if len(list) == 0 {
		return nil, nil
	}
	first, _ := list[0].(map[string]any)
	rawItems, _ := first["items"].([]any)
	var items []map[string]any
	for _, item := range rawItems {
		if m, ok := item.(map[string]any); ok {
			items = append(items, m)
		}
	}
	return items, nil
}

// AppLibraries lists Drive's app libraries: the containers apps write into
// (zone iCloud.<bundle id>), which the CloudDocs root listing does not
// hold.
func (a *API) AppLibraries() ([]map[string]any, error) {
	out, err := a.get(a.DriveBase+"/retrieveAppLibraries", a.DriveQ)
	if err != nil {
		return nil, err
	}
	// The items key must be there (an empty list is fine): a 200 carrying
	// an error body must not read as "no app libraries".
	env, ok := out.(map[string]any)
	raw, ok2 := env["items"].([]any)
	if !ok || !ok2 {
		b, _ := json.Marshal(out)
		return nil, fmt.Errorf("unexpected app library response envelope: %s", truncate(string(b), 200))
	}
	var items []map[string]any
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			items = append(items, m)
		}
	}
	return items, nil
}

// Child finds one entry by name: an exact match, else the one entry that
// matches ignoring case (libraries match the same way).
func (a *API) Child(drivewsid, name string) (map[string]any, error) {
	items, err := a.Items(drivewsid)
	if err != nil {
		return nil, err
	}
	// Drive keeps a file's extension out of its name, so a configured
	// "zen-habits" and "zen-habits.jsonl" both name zen-habits + jsonl.
	var folded []map[string]any
	for _, item := range items {
		if strOf(item["name"]) == name || fileName(item) == name {
			return item, nil
		}
		if strings.EqualFold(strOf(item["name"]), name) || strings.EqualFold(fileName(item), name) {
			folded = append(folded, item)
		}
	}
	if len(folded) == 1 {
		return folded[0], nil
	}
	return nil, nil
}

// Download turns a file's docwsid into bytes via a signed URL.
func (a *API) Download(item map[string]any) ([]byte, error) {
	// The item's own zone, from its zone field or its drivewsid: an app
	// container's files download through that container's zone.
	zone := zoneOf(item)
	if zone == "" {
		zone = "com.apple.CloudDocs"
	}
	docwsid, _ := item["docwsid"].(string)
	out, err := a.post(a.DocBase+"/ws/"+zone+"/download/batch", a.DocQ,
		[]any{map[string]any{"document_id": docwsid}})
	if err != nil {
		return nil, err
	}
	var url string
	if list, ok := out.([]any); ok && len(list) > 0 {
		if first, ok := list[0].(map[string]any); ok {
			if token, ok := first["data_token"].(map[string]any); ok {
				url, _ = token["url"].(string)
			}
		}
	}
	if url == "" {
		raw, _ := json.Marshal(out)
		return nil, fmt.Errorf("no download url for %v: %s", item["name"], truncate(string(raw), 200))
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// No cookies here: the signed URL already authorises the download, and
	// the content host gets no session it does not need.
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d downloading %v", resp.StatusCode, item["name"])
	}
	return io.ReadAll(resp.Body)
}

func truncate(s string, n int) string {
	return mcpserver.TruncateRunes(s, n)
}

var hostRe = regexp.MustCompile(`https://(p\d+-(drivews|docws)\.icloud\.com)/`)

// SplitRequest parses a captured request URL into service, base URL, and
// query, keeping the first host seen per service.
func SplitRequest(rawurl string) (service, base, query string, ok bool) {
	m := hostRe.FindStringSubmatch(rawurl)
	if m == nil {
		return "", "", "", false
	}
	q := ""
	if i := strings.Index(rawurl, "?"); i >= 0 {
		q = rawurl[i+1:]
	}
	return m[2], "https://" + m[1], q, true
}

// Stage writes data under the staging root at rel, atomically: partial
// files never look complete. rel must stay inside the root: names arrive
// from Drive listings, which are not trusted input.
func Stage(staging, rel string, data []byte) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe staging path: %q", rel)
	}
	for _, piece := range strings.Split(filepath.ToSlash(rel), "/") {
		if piece == ".." {
			return "", fmt.Errorf("unsafe staging path: %q", rel)
		}
	}
	target := filepath.Join(staging, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", err
	}
	tmp := target + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	return target, nil
}

// Run pulls every configured library. Quiet-hours and empty-library
// guards live in the cmd layer; here are the etag skips, per-kind walks,
// and the same exit codes (0 fetched, 1 nothing, 2 not runnable).
func Run(api *API, etags map[string]any, staging string, libs []dvlibraries.Library, out, errout *strings.Builder, today time.Time) int {
	fetched, skipped, failed := 0, 0, 0
	root, err := api.Items("FOLDER::com.apple.CloudDocs::root")
	if err != nil {
		fmt.Fprintf(errout, "drive root listing failed: %v\n", err)
		return 1
	}
	// App containers are not in the root listing. A failed app-library
	// listing still lets root libraries through, and says so.
	apps, appsErr := api.AppLibraries()
	if appsErr != nil {
		fmt.Fprintf(errout, "drive app library listing failed: %v\n", appsErr)
	}
	missing := false
	pull := func(item map[string]any, rel string) {
		key, _ := item["docwsid"].(string)
		if etag, _ := item["etag"].(string); key != "" && etag != "" {
			if old, _ := etags[key].(string); old == etag {
				// Same content as last time, and the last copy was
				// already handed over (the sync empties staging after
				// every run): skip. A staged file still waiting on the
				// sync is re-fetched instead; imports are idempotent.
				if _, err := os.Stat(filepath.Join(staging, rel)); os.IsNotExist(err) {
					skipped++
					fmt.Fprintf(out, "  %s: unchanged\n", rel)
					return
				}
			}
		}
		data, err := api.Download(item)
		if err != nil {
			failed++
			fmt.Fprintf(out, "  %s: %v\n", rel, err)
			return
		}
		if _, err := Stage(staging, rel, data); err != nil {
			failed++
			fmt.Fprintf(out, "  %s: %v\n", rel, err)
			return
		}
		if key != "" {
			etags[key] = item["etag"]
		}
		fetched++
		fmt.Fprintf(out, "  %s: %d bytes, modified %v\n", rel, len(data), item["dateModified"])
	}
	for _, lib := range libs {
		found, why := findLibrary(lib, root, apps)
		if found == nil && lib.Container != "" {
			// A container the app did not make public is in neither
			// listing, but its documents folder can still be listed by id.
			zone := containerZone(lib.Container)
			if items, err := api.Items("FOLDER::" + zone + "::documents"); err == nil && items != nil {
				found = map[string]any{"name": lib.Name, "type": "APP_LIBRARY", "zone": zone, "drivewsid": "FOLDER::" + zone + "::documents"}
			} else if err != nil {
				why += fmt.Sprintf("; listing its documents folder by id failed: %v", err)
			} else {
				why += "; its documents folder listed by id is empty or absent"
			}
		}
		if found == nil {
			fmt.Fprintf(errout, "%s %s\n", describe(lib), why)
			missing = true
			failed++
			continue
		}
		switch lib.Kind {
		case "snapshot":
			name := lib.File
			if name == "" {
				name = lib.Name
			}
			f, err := api.Child(strOf(found["drivewsid"]), name)
			if err != nil {
				fmt.Fprintf(errout, "%s: %v\n", lib.Name, err)
				failed++
				continue
			}
			if f == nil {
				// Say what the library does hold, as for a missing library.
				held := "(nothing)"
				if items, err := api.Items(strOf(found["drivewsid"])); err == nil && len(items) > 0 {
					var names []string
					for _, item := range items {
						n := strOf(item["name"])
						if ext := strOf(item["extension"]); ext != "" {
							n += "." + ext
						}
						names = append(names, fmt.Sprintf("%q %s", n, strOf(item["type"])))
					}
					held = strings.Join(names, "; ")
				}
				fmt.Fprintf(errout, "%s/%s not found; %s holds: %s\n", lib.Name, name, lib.Name, held)
				failed++
				continue
			}
			dest := lib.As
			if dest == "" {
				var serr error
				dest, serr = dvlibraries.StagingPath(lib.Dest, fileName(f))
				if serr != nil {
					fmt.Fprintf(errout, "%s: %v\n", lib.Name, serr)
					failed++
					continue
				}
			}
			pull(f, dest)
		case "folder":
			// An app export can have any layout. Keep it intact under
			// its configured staging directory.
			var walk func(drivewsid, prefix string) error
			walk = func(drivewsid, prefix string) error {
				items, err := api.Items(drivewsid)
				if err != nil {
					return err
				}
				for _, item := range items {
					name := fileName(item)
					childPath, serr := dvlibraries.StagingPath(prefix, name)
					if serr != nil {
						return serr
					}
					if item["type"] == "FOLDER" {
						id, _ := item["drivewsid"].(string)
						if err := walk(id, childPath); err != nil {
							return err
						}
						continue
					}
					pull(item, childPath)
				}
				return nil
			}
			if err := walk(strOf(found["drivewsid"]), lib.Dest); err != nil {
				fmt.Fprintf(errout, "%s: %v\n", lib.Name, err)
				failed++
			}
		default: // tree: per-metric folders holding monthly files YYYY-MM
			base := found
			if lib.Subfolder != "" {
				sub, err := api.Child(strOf(found["drivewsid"]), lib.Subfolder)
				if err != nil {
					fmt.Fprintf(errout, "%s: %v\n", lib.Name, err)
					failed++
					continue
				}
				if sub == nil {
					fmt.Fprintf(errout, "%s/%s not found\n", lib.Name, lib.Subfolder)
					failed++
					continue
				}
				base = sub
			}
			// With months set, sample files are limited to the newest N;
			// without it every month is considered and the etag skip keeps
			// unchanged ones free. Tombstone months are never limited: a
			// deletion dropped by the window leaves a stale sample, and a
			// delete-and-re-export burst deletes history whose new copies
			// only an unlimited pull brings back.
			var months []string
			if lib.Months > 0 {
				months = Months(lib.Months, today)
			}
			metrics, err := api.Items(strOf(base["drivewsid"]))
			if err != nil {
				fmt.Fprintf(errout, "%s: %v\n", lib.Name, err)
				failed++
				continue
			}
			for _, metric := range metrics {
				if metric["type"] != "FOLDER" {
					continue
				}
				files, err := api.Items(strOf(metric["drivewsid"]))
				if err != nil {
					fmt.Fprintf(errout, "%s: %v\n", lib.Name, err)
					failed++
					break
				}
				limit := months
				if strOf(metric["name"]) == "_tombstones" {
					limit = nil
				}
				for _, f := range files {
					month := strOf(f["name"])
					if f["type"] == "FOLDER" || !monthRe.MatchString(month) || (limit != nil && !contains(limit, month)) {
						continue
					}
					rel, serr := dvlibraries.StagingPath(lib.Dest, strOf(metric["name"]), month+".jsonl")
					if serr != nil {
						fmt.Fprintf(errout, "%s: %v\n", lib.Name, serr)
						failed++
						continue
					}
					pull(f, rel)
				}
			}
		}
	}
	if missing {
		// Once, after every library: what Drive did hold, so the miss
		// explains itself.
		fmt.Fprint(errout, listing(root, apps, appsErr))
	}
	fmt.Fprintf(out, "fetched %d, unchanged %d, failed %d, into %s\n", fetched, skipped, failed, staging)
	// Exit codes keep their meaning (0 something fetched or unchanged, 1
	// nothing): the scheduler outside this repo gates the import on them,
	// and a standing miss exiting 1 would stop every import. Misses are
	// loud on stderr instead.
	if fetched > 0 || skipped > 0 {
		return 0
	}
	return 1
}

// monthRe is a monthly file's name as Drive lists it: the extension is a
// separate field, so the name is the bare YYYY-MM.
var monthRe = regexp.MustCompile(`^\d{4}-\d{2}$`)

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// fileName is a Drive file's full name: the listing keeps the extension
// in its own field (live: name "zen-habits", extension "jsonl"), and a
// name without it would stage zen-habits and let a.csv and a.txt collide.
func fileName(item map[string]any) string {
	name := strOf(item["name"])
	if ext := strOf(item["extension"]); ext != "" && item["type"] != "FOLDER" && !strings.HasSuffix(name, "."+ext) {
		return name + "." + ext
	}
	return name
}

// zoneOf is an entry's zone: its zone field, else the middle part of a
// drivewsid shaped FOLDER::<zone>::<id>.
func zoneOf(item map[string]any) string {
	if z := strOf(item["zone"]); z != "" {
		return z
	}
	if parts := strings.Split(strOf(item["drivewsid"]), "::"); len(parts) == 3 {
		return parts[1]
	}
	return ""
}

// containerKey folds a bundle id or zone to one form: iCloud.com.x.App and
// com.x.App name the same container.
func containerKey(s string) string {
	// The Mac's folder name spells the zone with tildes:
	// iCloud~com~mlyz~HealthBridge is iCloud.com.mlyz.HealthBridge.
	s = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "~", ".")
	return strings.TrimPrefix(s, "icloud.")
}

// containerZone is the zone a configured container names, in its own
// spelling: Drive's zone names are case-sensitive, so containerKey's
// lowercase form is for comparing only.
func containerZone(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "~", ".")
	if !strings.HasPrefix(strings.ToLower(s), "icloud.") {
		s = "iCloud." + s
	}
	return s
}

// findLibrary picks the entry a configured library names. A container
// (bundle id or zone) wins over the display name, because an app rename
// changes the name and keeps the container; without one the display name
// matches case-insensitively. App libraries are searched with the root.
func findLibrary(lib dvlibraries.Library, root, apps []map[string]any) (map[string]any, string) {
	// One entry per drivewsid: an app library the root also lists (live:
	// "Zen - Habits" is in both) is one library, not two.
	var all []map[string]any
	seen := map[string]bool{}
	for _, item := range append(append([]map[string]any{}, root...), apps...) {
		id := strOf(item["drivewsid"])
		if id != "" && seen[id] {
			continue
		}
		seen[id] = true
		all = append(all, item)
	}
	if lib.Container != "" {
		want := containerKey(lib.Container)
		for _, item := range all {
			if z := zoneOf(item); z != "" && containerKey(z) == want && containerKey(z) != "com.apple.clouddocs" {
				return item, ""
			}
		}
		return nil, "not found: no app library has that container"
	}
	// An exact name wins; otherwise one case-insensitive match. Two
	// entries that both fit (a root folder and an app library sharing a
	// name) is ambiguous: say so rather than pull the wrong one.
	var exact, folded []map[string]any
	for _, item := range all {
		name := strings.TrimSpace(strOf(item["name"]))
		if name == strings.TrimSpace(lib.Name) {
			exact = append(exact, item)
		} else if strings.EqualFold(name, strings.TrimSpace(lib.Name)) {
			folded = append(folded, item)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], ""
	case len(exact) > 1:
		return nil, "is ambiguous: several Drive entries have that name; add \"container\" to pick one"
	case len(folded) == 1:
		return folded[0], ""
	case len(folded) > 1:
		return nil, "is ambiguous: several Drive entries match that name ignoring case; add \"container\""
	}
	return nil, "not found at the Drive root or among the app libraries"
}

func describe(lib dvlibraries.Library) string {
	if lib.Container != "" {
		return fmt.Sprintf("%s (container %s)", lib.Name, lib.Container)
	}
	return lib.Name
}

// listing says what Drive did hold, so a missing library explains itself:
// names and types at the root, names and zones among the app libraries.
func listing(root, apps []map[string]any, appsErr error) string {
	line := func(items []map[string]any, zone bool) string {
		if len(items) == 0 {
			return "(none)"
		}
		var parts []string
		for _, item := range items {
			p := fmt.Sprintf("%q %s", strOf(item["name"]), strOf(item["type"]))
			if zone {
				p += " " + zoneOf(item)
			}
			parts = append(parts, strings.TrimSpace(p))
		}
		return strings.Join(parts, "; ")
	}
	appLine := line(apps, true)
	if appsErr != nil {
		appLine = "unavailable: " + appsErr.Error()
	}
	return "  the Drive root holds: " + line(root, false) + "\n  the app libraries are: " + appLine + "\n"
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}
