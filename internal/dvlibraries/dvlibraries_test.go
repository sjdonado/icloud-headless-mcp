package dvlibraries

import "testing"

func TestValid(t *testing.T) {
	if !Valid("Example Exports", "folder", "exports", "") {
		t.Error("valid folder library rejected")
	}
	if Valid("Example Exports", "folder", "../exports", "") {
		t.Error("parent-directory dest accepted")
	}
	if Valid("Example Exports", "folder", ".", "") {
		t.Error("dot dest accepted")
	}
	if Valid("Example Export", "snapshot", "exports", "/tmp/export") {
		t.Error("absolute as accepted")
	}
	if Valid("Example Export", "bogus", "exports", "") {
		t.Error("unknown kind accepted")
	}
	if Valid("", "folder", "exports", "") {
		t.Error("empty name accepted")
	}
}

func TestStagingPath(t *testing.T) {
	got, err := StagingPath("exports", "daily", "report.csv")
	if err != nil || got != "exports/daily/report.csv" {
		t.Fatalf("StagingPath = %q, %v", got, err)
	}
	if _, err := StagingPath("exports", "../report.csv"); err == nil {
		t.Fatal("parent component accepted")
	}
	if _, err := StagingPath("exports", ""); err == nil {
		t.Fatal("empty component accepted")
	}
}

func TestPullFolderPreservesNestedPaths(t *testing.T) {
	items := map[string][]Folder{
		"root": {
			{DriveWSID: "reports", Name: "reports", IsFolder: true},
			{DocWSID: "index", Name: "index.json"},
		},
		"reports": {
			{DriveWSID: "2026", Name: "2026", IsFolder: true},
		},
		"2026": {
			{DocWSID: "summary", Name: "summary.csv"},
		},
	}
	var pulled [][2]string
	err := PullFolder(func(id string) []Folder { return items[id] },
		Folder{DriveWSID: "root"}, "example-app",
		func(item Folder, stagingPath string) {
			pulled = append(pulled, [2]string{item.DocWSID, stagingPath})
		})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{
		{"summary", "example-app/reports/2026/summary.csv"},
		{"index", "example-app/index.json"},
	}
	if len(pulled) != len(want) {
		t.Fatalf("pulled = %v", pulled)
	}
	for i := range want {
		if pulled[i] != want[i] {
			t.Fatalf("pulled = %v, want %v", pulled, want)
		}
	}
}

func TestParseLibrariesBadValuePullsNothing(t *testing.T) {
	for _, raw := range []string{"", "not json", "{}", `[{"name": "x"}]`, "[]"} {
		if got := ParseLibraries(raw); len(got) != 0 {
			t.Fatalf("ParseLibraries(%q) = %v, want empty", raw, got)
		}
	}
	got := ParseLibraries(`[{"name": "Example App Exports", "kind": "folder", "dest": "example-app"}]`)
	if len(got) != 1 || got[0].Dest != "example-app" || got[0].Kind != "folder" {
		t.Fatalf("ParseLibraries = %+v", got)
	}
}
