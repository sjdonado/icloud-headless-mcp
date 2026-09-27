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
	// apps is what retrieveAppLibraries returns; nil answers 404, as a
	// server without the endpoint would.
	apps []map[string]any
	// zones records the zone of every download/batch call.
	zones []string
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
		if items == nil && strings.HasPrefix(id, "FOLDER::iCloud.") {
			// Live: an unknown container's folder answers 502 ZONE_NOT_FOUND.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"errorReason":"ZONE_NOT_FOUND","errorCode":502}`))
			return
		}
		if items == nil {
			items = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode([]any{map[string]any{"items": items}})
	})
	mux.HandleFunc("/retrieveAppLibraries", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if f.apps == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": f.apps})
	})
	mux.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		zone := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ws/"), "/download/batch")
		f.zones = append(f.zones, zone)
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

// healthBridge is an app container as the brief describes it on the
// owner's Mac: iCloud~com~mlyz~HealthBridge/Documents/raw/<metric>/ plus
// raw/_tombstones/, listed by retrieveAppLibraries, not the CloudDocs root.
func healthBridge(f *fakeDrive) {
	const zone = "iCloud.com.mlyz.HealthBridge"
	f.apps = []map[string]any{
		{"name": "HealthBridge", "type": "APP_LIBRARY", "zone": zone, "drivewsid": "FOLDER::" + zone + "::documents"},
	}
	f.folders["FOLDER::"+zone+"::documents"] = []map[string]any{
		{"name": "raw", "type": "FOLDER", "zone": zone, "drivewsid": "FOLDER::" + zone + "::raw"},
	}
	f.folders["FOLDER::"+zone+"::raw"] = []map[string]any{
		{"name": "steps", "type": "FOLDER", "zone": zone, "drivewsid": "FOLDER::" + zone + "::steps"},
		{"name": "_tombstones", "type": "FOLDER", "zone": zone, "drivewsid": "FOLDER::" + zone + "::tomb"},
	}
	f.folders["FOLDER::"+zone+"::steps"] = []map[string]any{
		{"name": "2026-09", "extension": "jsonl", "type": "FILE", "zone": zone, "docwsid": "doc-steps", "etag": "s1"},
	}
	f.folders["FOLDER::"+zone+"::tomb"] = []map[string]any{
		{"name": "2026-09", "extension": "jsonl", "type": "FILE", "zone": zone, "docwsid": "doc-tomb", "etag": "t1"},
	}
	f.files["doc-steps"] = []byte("{}\n")
	f.files["doc-tomb"] = []byte("{}\n")
}

func TestRunFindsAppLibraryByContainerAfterRename(t *testing.T) {
	f, api := newFakeDrive(t)
	healthBridge(f)
	staging := t.TempDir()
	var out, errout strings.Builder
	today := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	// Configured under the app's old name: the container still matches.
	libs := []dvlibraries.Library{{Name: "HealthMirror", Container: "com.mlyz.HealthBridge", Kind: "tree",
		Dest: "health", Subfolder: "raw", Months: 1}}
	if code := Run(api, map[string]any{}, staging, libs, &out, &errout, today); code != 0 {
		t.Fatalf("exit %d: out=%s err=%s", code, out.String(), errout.String())
	}
	for _, rel := range []string{"health/steps/2026-09.jsonl", "health/_tombstones/2026-09.jsonl"} {
		if _, err := os.Stat(filepath.Join(staging, rel)); err != nil {
			t.Errorf("missing staged file %s: %v", rel, err)
		}
	}
	if len(f.zones) != 2 {
		t.Fatalf("download calls = %v, want the two files", f.zones)
	}
	for _, z := range f.zones {
		if z != "iCloud.com.mlyz.HealthBridge" {
			t.Errorf("downloaded through zone %q, want the container's own", z)
		}
	}
}

func TestRunFindsAppLibraryByDisplayName(t *testing.T) {
	f, api := newFakeDrive(t)
	healthBridge(f)
	var out, errout strings.Builder
	today := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	libs := []dvlibraries.Library{{Name: "healthbridge", Kind: "tree", Dest: "health", Subfolder: "raw", Months: 1}}
	if code := Run(api, map[string]any{}, t.TempDir(), libs, &out, &errout, today); code != 0 {
		t.Fatalf("a case-insensitive display name should match: %s", errout.String())
	}
}

func TestRunMissingLibraryListsWhatDriveHeld(t *testing.T) {
	f, api := newFakeDrive(t)
	healthBridge(f)
	var out, errout strings.Builder
	// The old name, with no container: the rename breaks it, loudly.
	libs := []dvlibraries.Library{{Name: "HealthMirror", Kind: "tree", Dest: "health", Subfolder: "raw", Months: 1}}
	if code := Run(api, map[string]any{}, t.TempDir(), libs, &out, &errout, time.Now()); code == 0 {
		t.Fatal("a missing library exited 0")
	}
	msg := errout.String()
	for _, want := range []string{"HealthMirror not found", `"Monthly" FOLDER`, `"HealthBridge" APP_LIBRARY iCloud.com.mlyz.HealthBridge`} {
		if !strings.Contains(msg, want) {
			t.Errorf("not-found message lacks %q:\n%s", want, msg)
		}
	}
}

func TestContainerNeverMatchesCloudDocs(t *testing.T) {
	root := []map[string]any{{"name": "HealthBridge", "type": "FOLDER", "drivewsid": "FOLDER::com.apple.CloudDocs::abc"}}
	lib := dvlibraries.Library{Name: "x", Container: "com.apple.CloudDocs", Kind: "tree", Dest: "d"}
	if item, _ := findLibrary(lib, root, nil); item != nil {
		t.Fatal("a container must name an app container, never the shared CloudDocs zone")
	}
	lib = dvlibraries.Library{Name: "x", Container: "iCloud.com.mlyz.HealthBridge", Kind: "tree", Dest: "d"}
	apps := []map[string]any{{"name": "HealthBridge", "drivewsid": "FOLDER::iCloud.com.mlyz.HealthBridge::documents"}}
	if item, _ := findLibrary(lib, nil, apps); item == nil {
		t.Fatal("the zone parsed from a drivewsid should match a zone-form container")
	}
}

func TestFindLibraryExactNameWinsAndAmbiguityRefuses(t *testing.T) {
	root := []map[string]any{{"name": "HealthBridge", "type": "FOLDER", "drivewsid": "FOLDER::com.apple.CloudDocs::a"}}
	apps := []map[string]any{{"name": "HealthBridge", "type": "APP_LIBRARY", "drivewsid": "FOLDER::iCloud.com.mlyz.HealthBridge::documents"}}
	if item, why := findLibrary(dvlibraries.Library{Name: "HealthBridge"}, root, apps); item != nil || !strings.Contains(why, "ambiguous") {
		t.Fatalf("a root folder and an app library sharing a name = %v, %q; want ambiguous", item, why)
	}
	if item, _ := findLibrary(dvlibraries.Library{Name: "HealthBridge", Container: "com.mlyz.HealthBridge"}, root, apps); item == nil || zoneOf(item) != "iCloud.com.mlyz.HealthBridge" {
		t.Fatalf("container should pick the app library, got %v", item)
	}
	folded := []map[string]any{{"name": "Healthbridge", "drivewsid": "FOLDER::com.apple.CloudDocs::b"}}
	if item, _ := findLibrary(dvlibraries.Library{Name: "HealthBridge"}, folded, apps); item == nil || zoneOf(item) != "iCloud.com.mlyz.HealthBridge" {
		t.Fatalf("an exact name should beat a case-insensitive one, got %v", item)
	}
}

func TestAppLibraryListingFailureIsNotEmpty(t *testing.T) {
	_, api := newFakeDrive(t) // apps nil: the endpoint answers 404
	var out, errout strings.Builder
	libs := []dvlibraries.Library{{Name: "Nope", Kind: "folder", Dest: "nope"}, {Name: "Nope2", Kind: "folder", Dest: "nope2"}}
	Run(api, map[string]any{}, t.TempDir(), libs, &out, &errout, time.Now())
	msg := errout.String()
	if !strings.Contains(msg, "the app libraries are: unavailable:") {
		t.Fatalf("a failed listing must not read as empty:\n%s", msg)
	}
	if strings.Count(msg, "the Drive root holds:") != 1 {
		t.Fatalf("the listing should print once for all missing libraries:\n%s", msg)
	}
}

func TestTombstonesIgnoreTheMonthsWindow(t *testing.T) {
	f, api := newFakeDrive(t)
	healthBridge(f)
	const zone = "iCloud.com.mlyz.HealthBridge"
	// An old tombstone month and an old sample month, outside a 1-month
	// window; the old sample file has no zone field, only its drivewsid.
	f.folders["FOLDER::"+zone+"::tomb"] = append(f.folders["FOLDER::"+zone+"::tomb"],
		map[string]any{"name": "2026-05", "type": "FILE", "zone": zone, "docwsid": "doc-tomb-may", "etag": "t5"})
	f.folders["FOLDER::"+zone+"::steps"] = append(f.folders["FOLDER::"+zone+"::steps"],
		map[string]any{"name": "2026-05", "type": "FILE", "drivewsid": "FILE::" + zone + "::steps-may", "docwsid": "doc-steps-may", "etag": "s5"})
	f.files["doc-tomb-may"] = []byte("{}\n")
	f.files["doc-steps-may"] = []byte("{}\n")
	today := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)

	windowed := t.TempDir()
	var out, errout strings.Builder
	lib := dvlibraries.Library{Name: "HealthBridge", Container: "iCloud~com~mlyz~HealthBridge", Kind: "tree", Dest: "health", Subfolder: "raw", Months: 1}
	if code := Run(api, map[string]any{}, windowed, []dvlibraries.Library{lib}, &out, &errout, today); code != 0 {
		t.Fatalf("exit %d: %s", code, errout.String())
	}
	if _, err := os.Stat(filepath.Join(windowed, "health/_tombstones/2026-05.jsonl")); err != nil {
		t.Error("a tombstone month outside the window was dropped")
	}
	if _, err := os.Stat(filepath.Join(windowed, "health/steps/2026-05.jsonl")); !os.IsNotExist(err) {
		t.Error("a sample month outside the window was pulled despite months=1")
	}

	f.zones = nil
	all := t.TempDir()
	lib.Months = 0
	if code := Run(api, map[string]any{}, all, []dvlibraries.Library{lib}, &out, &errout, today); code != 0 {
		t.Fatalf("exit %d: %s", code, errout.String())
	}
	if _, err := os.Stat(filepath.Join(all, "health/steps/2026-05.jsonl")); err != nil {
		t.Error("without months, every sample month should be pulled")
	}
	for _, z := range f.zones {
		if z != zone {
			t.Errorf("a file without a zone field downloaded through %q; its drivewsid names %s", z, zone)
		}
	}
}

func TestContainerFallbackByIDKeepsItsCase(t *testing.T) {
	f, api := newFakeDrive(t)
	const zone = "iCloud.com.mlyz.HealthBridge"
	// In neither listing: only its documents folder answers, and only
	// under the zone's real case (the fake answers ZONE_NOT_FOUND else).
	f.apps = []map[string]any{}
	f.folders["FOLDER::"+zone+"::documents"] = []map[string]any{
		{"name": "note.txt", "type": "FILE", "zone": zone, "docwsid": "doc-n", "etag": "n1"},
	}
	f.files["doc-n"] = []byte("x")
	var out, errout strings.Builder
	libs := []dvlibraries.Library{{Name: "HealthBridge", Container: "com.mlyz.HealthBridge", Kind: "folder", Dest: "hb"}}
	if code := Run(api, map[string]any{}, t.TempDir(), libs, &out, &errout, time.Now()); code != 0 {
		t.Fatalf("the by-id fallback should find the container: %s", errout.String())
	}
	libs[0].Container = "com.example.Missing"
	errout.Reset()
	Run(api, map[string]any{}, t.TempDir(), libs, &out, &errout, time.Now())
	if !strings.Contains(errout.String(), "ZONE_NOT_FOUND") {
		t.Fatalf("a container the account lacks should say ZONE_NOT_FOUND: %s", errout.String())
	}
}

// Live on 2026-09-27: "Zen - Habits" is listed at the root and among the
// app libraries with one drivewsid. That is one library, not an ambiguity.
func TestSameEntryInBothListingsIsOneLibrary(t *testing.T) {
	zen := map[string]any{"name": "Zen - Habits", "type": "APP_LIBRARY", "zone": "iCloud.com.zenhabit.app",
		"drivewsid": "FOLDER::iCloud.com.zenhabit.app::documents"}
	if item, why := findLibrary(dvlibraries.Library{Name: "Zen - Habits"}, []map[string]any{zen}, []map[string]any{zen}); item == nil {
		t.Fatalf("found nothing: %s", why)
	}
}

// Live 2026-09-27: "Zen - Habits" holds "@container" and a file Drive lists
// as name "zen-habits", extension "jsonl". It stages as zen-habits.jsonl,
// whether the entry names the file with or without its extension.
func TestFilesStageWithTheirExtension(t *testing.T) {
	f, api := newFakeDrive(t)
	const zone = "iCloud.com.zenhabit.app"
	f.apps = []map[string]any{{"name": "Zen - Habits", "type": "APP_LIBRARY", "zone": zone, "drivewsid": "FOLDER::" + zone + "::documents"}}
	f.folders["FOLDER::"+zone+"::documents"] = []map[string]any{
		{"name": "@container", "type": "FOLDER", "zone": zone, "drivewsid": "FOLDER::" + zone + "::c"},
		{"name": "zen-habits", "extension": "jsonl", "type": "FILE", "zone": zone, "docwsid": "doc-zen", "etag": "z1"},
	}
	f.folders["FOLDER::"+zone+"::c"] = []map[string]any{}
	f.files["doc-zen"] = []byte("{}\n")
	for _, file := range []string{"zen-habits", "zen-habits.jsonl"} {
		staging := t.TempDir()
		var out, errout strings.Builder
		libs := []dvlibraries.Library{{Name: "Zen - Habits", Container: zone, Kind: "snapshot", File: file, Dest: "zen"}}
		if code := Run(api, map[string]any{}, staging, libs, &out, &errout, time.Now()); code != 0 {
			t.Fatalf("file %q: exit %d: %s", file, code, errout.String())
		}
		if _, err := os.Stat(filepath.Join(staging, "zen/zen-habits.jsonl")); err != nil {
			t.Errorf("file %q did not stage as zen/zen-habits.jsonl: %v", file, err)
		}
	}
	staging := t.TempDir()
	var out, errout strings.Builder
	libs := []dvlibraries.Library{{Name: "Zen - Habits", Container: zone, Kind: "folder", Dest: "zen"}}
	if code := Run(api, map[string]any{}, staging, libs, &out, &errout, time.Now()); code != 0 {
		t.Fatalf("folder walk: exit %d: %s", code, errout.String())
	}
	if _, err := os.Stat(filepath.Join(staging, "zen/zen-habits.jsonl")); err != nil {
		t.Errorf("the folder walk dropped the extension: %v", err)
	}
}
