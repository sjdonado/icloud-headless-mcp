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
	list, _ := out.([]any)
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

// Child finds one entry by name.
func (a *API) Child(drivewsid, name string) (map[string]any, error) {
	items, err := a.Items(drivewsid)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item["name"] == name {
			return item, nil
		}
	}
	return nil, nil
}

// Download turns a file's docwsid into bytes via a signed URL.
func (a *API) Download(item map[string]any) ([]byte, error) {
	zone, _ := item["zone"].(string)
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
	if a.Cookies != "" {
		req.Header.Set("Cookie", a.Cookies)
	}
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
// files never look complete.
func Stage(staging, rel string, data []byte) (string, error) {
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
		var found map[string]any
		for _, item := range root {
			if item["name"] == lib.Name {
				found = item
			}
		}
		if found == nil {
			fmt.Fprintf(errout, "%s not found at the Drive root\n", lib.Name)
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
				fmt.Fprintf(errout, "%s/%s not found\n", lib.Name, name)
				failed++
				continue
			}
			dest := lib.As
			if dest == "" {
				var serr error
				dest, serr = dvlibraries.StagingPath(lib.Dest, name)
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
					name, _ := item["name"].(string)
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
			months := Months(lib.Months, today)
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
				for _, month := range months {
					for _, f := range files {
						if f["name"] != month {
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
	}
	fmt.Fprintf(out, "fetched %d, unchanged %d, failed %d, into %s\n", fetched, skipped, failed, staging)
	if fetched > 0 || skipped > 0 {
		return 0
	}
	return 1
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}
