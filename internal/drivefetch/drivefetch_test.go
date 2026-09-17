package drivefetch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/dvlibraries"
)

// fakeDrive serves retrieveItemDetailsInFolders, download/batch, and file
// bytes from scripted fixtures.
type fakeDrive struct {
	folders map[string][]map[string]any
	files   map[string][]byte
}

func newFakeDrive(t *testing.T) (*fakeDrive, *API) {
	t.Helper()
	f := &fakeDrive{
		folders: map[string][]map[string]any{
			"root": {
				{"name": "Example App Exports", "type": "FOLDER", "drivewsid": "lib1"},
				{"name": "Monthly", "type": "FOLDER", "drivewsid": "tree1"},
				{"name": "Snap", "type": "FOLDER", "drivewsid": "snap1"},
			},
			"lib1": {
				{"name": "reports", "type": "FOLDER", "drivewsid": "reports"},
				{"name": "index.json", "type": "FILE", "docwsid": "doc-index",
					"etag": "e1", "dateModified": "2026-01-01"},
			},
			"reports": {
				{"name": "r.csv", "type": "FILE", "docwsid": "doc-r",
					"etag": "e2", "dateModified": "2026-01-02"},
			},
			"tree1": {
				{"name": "weight", "type": "FOLDER", "drivewsid": "metric1"},
			},
			"metric1": {
				{"name": "2026-09", "type": "FILE", "docwsid": "doc-sept",
					"etag": "e3", "dateModified": "2026-10-01"},
				{"name": "2026-08", "type": "FILE", "docwsid": "doc-aug",
					"etag": "e4", "dateModified": "2026-09-01"},
			},
			"snap1": {
				{"name": "export", "type": "FILE", "docwsid": "doc-snap",
					"etag": "e5", "dateModified": "2026-10-02"},
			},
		},
		files: map[string][]byte{
			"doc-index": []byte(`{"i":0}`),
			"doc-r":     []byte("a,b\n"),
			"doc-sept":  []byte("sept\n"),
			"doc-aug":   []byte("aug\n"),
			"doc-snap":  []byte("snap\n"),
		},
	}
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/retrieveItemDetailsInFolders", func(w http.ResponseWriter, r *http.Request) {
		var body []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		id, _ := body[0]["drivewsid"].(string)
		if id == "FOLDER::com.apple.CloudDocs::root" {
			id = "root"
		}
		if id == "block:" {
			// placeholder, never matched
		}
		items := f.folders[id]
		if items == nil {
			items = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"items": items}})
	})
	mux.HandleFunc("/ws/com.apple.CloudDocs/download/batch", func(w http.ResponseWriter, r *http.Request) {
		var body []map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		id, _ := body[0]["document_id"].(string)
		_ = json.NewEncoder(w).Encode([]any{map[string]any{
			"data_token": map[string]any{"url": base + "/file/" + id},
		}})
	})
	mux.HandleFunc("/file/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/file/")
		data, ok := f.files[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL
	return f, &API{
		DriveBase: srv.URL, DriveQ: "q=1",
		DocBase: srv.URL, DocQ: "q=2",
		Client: srv.Client(),
	}
}

func testLibs() []dvlibraries.Library {
	return []dvlibraries.Library{
		{Name: "Example App Exports", Kind: "folder", Dest: "example-app"},
		{Name: "Monthly", Kind: "tree", Dest: "monthly", Subfolder: "", Months: 2},
		{Name: "Snap", Kind: "snapshot", Dest: "snap", File: "export", As: "snap/export.jsonl"},
	}
}

func TestRunFetchesAllKinds(t *testing.T) {
	_, api := newFakeDrive(t)
	staging := t.TempDir()
	etags := map[string]any{}
	var out, errout strings.Builder
	// Pin "today" so the tree layout takes only 2026-09.
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	if code := Run(api, etags, staging, testLibs(), &out, &errout, today); code != 0 {
		t.Fatalf("exit %d: out=%s err=%s", code, out.String(), errout.String())
	}
	for _, rel := range []string{
		"example-app/index.json",
		"example-app/reports/r.csv",
		"monthly/weight/2026-09.jsonl",
		"snap/export.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(staging, rel)); err != nil {
			t.Errorf("missing staged file %s: %v", rel, err)
		}
	}
	// August is outside the one-month window.
	if _, err := os.Stat(filepath.Join(staging, "monthly/weight/2026-08.jsonl")); !os.IsNotExist(err) {
		t.Error("out-of-window month was pulled")
	}
	if len(etags) != 4 {
		t.Fatalf("etags = %v", etags)
	}
	if !strings.Contains(out.String(), "fetched 4, unchanged 0, failed 0") {
		t.Fatalf("summary: %s", out.String())
	}
}

func TestRunSkipsUnchanged(t *testing.T) {
	_, api := newFakeDrive(t)
	staging := t.TempDir()
	etags := map[string]any{}
	var out, errout strings.Builder
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	// First pass stages everything; the sync then empties staging.
	if code := Run(api, etags, staging, testLibs(), &out, &errout, today); code != 0 {
		t.Fatalf("first pass exit %d", code)
	}
	if err := os.RemoveAll(staging); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errout.Reset()
	if code := Run(api, etags, staging, testLibs(), &out, &errout, today); code != 0 {
		t.Fatalf("second pass exit %d: %s %s", code, out.String(), errout.String())
	}
	if !strings.Contains(out.String(), "fetched 0, unchanged 4, failed 0") {
		t.Fatalf("summary: %s", out.String())
	}
}

func TestRunMissingLibrary(t *testing.T) {
	_, api := newFakeDrive(t)
	staging := t.TempDir()
	var out, errout strings.Builder
	libs := []dvlibraries.Library{{Name: "Nope", Kind: "folder", Dest: "nope"}}
	if code := Run(api, map[string]any{}, staging, libs, &out, &errout, time.Now()); code == 0 {
		t.Fatal("missing library exited 0")
	}
	if !strings.Contains(errout.String(), "not found at the Drive root") {
		t.Fatalf("errout = %q", errout.String())
	}
}

func TestMonths(t *testing.T) {
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	got := Months(2, today)
	if len(got) != 2 || got[0] != "2026-10" || got[1] != "2026-09" {
		t.Fatalf("got %v", got)
	}
	if got := Months(0, today); len(got) != 1 {
		t.Fatalf("zero count must yield one month: %v", got)
	}
	// Year boundary steps back correctly.
	newYear := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	if got := Months(2, newYear); got[0] != "2026-01" || got[1] != "2025-12" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitRequest(t *testing.T) {
	svc, base, q, ok := SplitRequest("https://p12-drivews.icloud.com/retrieveItemDetailsInFolders?clientBuildNumber=1&dsid=2")
	if !ok || svc != "drivews" || base != "https://p12-drivews.icloud.com" || q != "clientBuildNumber=1&dsid=2" {
		t.Fatalf("got %q %q %q %v", svc, base, q, ok)
	}
	if _, _, _, ok := SplitRequest("https://www.icloud.com/notes/"); ok {
		t.Fatal("non-API URL matched")
	}
}
