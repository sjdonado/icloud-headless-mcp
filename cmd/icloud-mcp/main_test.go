package main

import "testing"

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
		"serve", "session-check", "resident", "login",
		"reask", "drain", "tab-reaper", "drive-fetch",
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
