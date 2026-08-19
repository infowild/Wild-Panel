package main

import "testing"

func TestParseHostSwitchesUninstallForms(t *testing.T) {
	cases := [][]string{
		{"--uninstall"},
		{"-uninstall"},
		{"uninstall"},
		{"--uninstall", "--yes"},
		{"--uninstall", "-y"},
		{"--yes", "--uninstall"},
		{"--uninstall", "unexpected"},
	}
	for _, args := range cases {
		h := parseHostSwitches(args)
		if !h.uninstall {
			t.Fatalf("uninstall not detected for %q", args)
		}
	}
	h := parseHostSwitches([]string{"--uninstall", "--yes"})
	if !h.force {
		t.Fatal("--yes should skip the confirm prompt")
	}
	h = parseHostSwitches([]string{"--user", "admin"})
	if h.uninstall || !h.hasExplicit || h.user != "admin" {
		t.Fatalf("user switch: %+v", h)
	}
}

func TestHostSwitchKey(t *testing.T) {
	if got := hostSwitchKey("--uninstall"); got != "uninstall" {
		t.Fatalf("got %q", got)
	}
	if got := hostSwitchKey("-uninstall"); got != "uninstall" {
		t.Fatalf("single dash: got %q", got)
	}
	if got := hostSwitchKey("uninstall"); got != "uninstall" {
		t.Fatalf("bare: got %q", got)
	}
}
