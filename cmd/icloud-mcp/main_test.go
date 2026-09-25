package main

import (
	"strings"
	"testing"
)

func TestDispatch(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"help"}, 0},
		{[]string{"-h"}, 0},
		{[]string{"--help"}, 0},
		{[]string{"version"}, 0},
		{[]string{"bogus"}, 2},
		{[]string{"tab-reaper", "bogus"}, 2},
	} {
		if got := run(tc.args); got != tc.want {
			t.Errorf("run(%v) = %d, want %d", tc.args, got, tc.want)
		}
	}
}

func TestSubcommandsComplete(t *testing.T) {
	want := []string{
		"serve", "session-check", "resident", "login", "door",
		"reask", "drain", "tab-reaper", "drive-fetch", "health-import",
	}
	if len(subcommands) != len(want) {
		t.Errorf("subcommands has %d entries, want %d", len(subcommands), len(want))
	}
	for _, name := range want {
		if _, ok := subcommands[name]; !ok {
			t.Errorf("subcommand %q missing from dispatch", name)
		}
	}
}

func TestChromeUA(t *testing.T) {
	ua, err := chromeUA("Google Chrome 145.0.7632.117 \n", "darwin")
	if err != nil || ua != "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36" {
		t.Fatalf("darwin: %q %v", ua, err)
	}
	ua, err = chromeUA("Chromium 140.0.7339.80 built on Debian", "linux")
	if err != nil || !strings.Contains(ua, "(X11; Linux x86_64)") || !strings.Contains(ua, "Chrome/140.0.0.0 ") {
		t.Fatalf("linux: %q %v", ua, err)
	}
	if _, err := chromeUA("garbage", "linux"); err == nil {
		t.Fatal("no version must refuse rather than guess")
	}
}
