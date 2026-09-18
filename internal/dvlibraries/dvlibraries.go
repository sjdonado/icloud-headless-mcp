// Package dvlibraries is which iCloud Drive libraries this install pulls,
// and where their files land.
//
// An "app library" is a folder at the Drive root that some app on the
// owner's phone writes into. Which ones exist is a fact about that phone,
// not about this server, so the list is configuration: DRIVE_LIBRARIES in
// this service's env file, a JSON array of entries.
package dvlibraries

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Library is one configured Drive library.
type Library struct {
	Name      string
	Kind      string // tree, snapshot, or folder
	Dest      string
	Subfolder string
	Months    int
	File      string
	As        string
}

// Valid reports whether an entry is usable. Dest (and As, when present)
// must be relative and free of ".." because they are joined onto the
// staging path, and this value arrives from a file rather than from code.
func Valid(name, kind, dest, as string) bool {
	if name == "" {
		return false
	}
	if kind != "tree" && kind != "snapshot" && kind != "folder" {
		return false
	}
	if !relative(dest) {
		return false
	}
	if as != "" && !relative(as) {
		return false
	}
	return true
}

// norm splits a configured path the way PurePath.parts does: duplicate
// slashes and single dots vanish, anything else is kept for the checks.
func norm(value string) []string {
	var out []string
	for _, piece := range strings.Split(value, "/") {
		if piece == "" || piece == "." {
			continue
		}
		out = append(out, piece)
	}
	return out
}

func relative(value string) bool {
	if value == "" {
		return false
	}
	if path.IsAbs(value) {
		return false
	}
	parts := norm(value)
	if len(parts) == 0 {
		return false
	}
	for _, piece := range parts {
		if piece == ".." {
			return false
		}
	}
	return true
}

// StagingPath is a safe relative staging path made from configured and
// Drive-provided names. Anything absolute, empty, or escaping is an error.
func StagingPath(parts ...string) (string, error) {
	var out []string
	for _, part := range parts {
		if part == "" {
			return "", fmt.Errorf("unsafe Drive path component: %q", part)
		}
		if path.IsAbs(part) {
			return "", fmt.Errorf("unsafe Drive path component: %q", part)
		}
		for _, piece := range strings.Split(part, "/") {
			if piece == "" || piece == "." {
				continue
			}
			if piece == ".." {
				return "", fmt.Errorf("unsafe Drive path component: %q", part)
			}
			out = append(out, piece)
		}
	}
	return path.Join(out...), nil
}

// Folder is one Drive folder's children, by drivewsid.
type Folder struct {
	DriveWSID string
	Name      string
	IsFolder  bool
	DocWSID   string
}

// PullFolder walks one Drive folder and hands each file to pull under its
// staging path, preserving the relative folder layout.
func PullFolder(items func(driveWSID string) []Folder, folder Folder, prefix string, pull func(item Folder, stagingPath string)) error {
	for _, child := range items(folder.DriveWSID) {
		childPath, err := StagingPath(prefix, child.Name)
		if err != nil {
			return err
		}
		if child.IsFolder {
			if err := PullFolder(items, child, childPath, pull); err != nil {
				return err
			}
			continue
		}
		pull(child, childPath)
	}
	return nil
}

// ParseLibraries parses the DRIVE_LIBRARIES value. A bad value pulls
// nothing: unparseable JSON, a non-list, or invalid entries yield an empty
// list rather than guesses. Never raises.
func ParseLibraries(raw string) []Library {
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	var out []Library
	for _, e := range parsed {
		name, _ := e["name"].(string)
		kind, _ := e["kind"].(string)
		dest, _ := e["dest"].(string)
		as, _ := e["as"].(string)
		if !Valid(name, kind, dest, as) {
			continue
		}
		lib := Library{Name: name, Kind: kind, Dest: dest, As: as}
		lib.Subfolder, _ = e["subfolder"].(string)
		lib.File, _ = e["file"].(string)
		if months, ok := e["months"].(float64); ok {
			lib.Months = int(months)
		}
		out = append(out, lib)
	}
	return out
}
