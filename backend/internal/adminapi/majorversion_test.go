package adminapi

import "testing"

func TestMajorOf(t *testing.T) {
	cases := map[string]int{"v1.2.3": 1, "2.0.0": 2, "V10.0": 10, "v2.0.0-rc.1": 2, "v2026.11.05": 2026}
	for in, want := range cases {
		if got, ok := majorOf(in); !ok || got != want {
			t.Fatalf("majorOf(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "v", "release-1", "v.x.1"} {
		if _, ok := majorOf(bad); ok {
			t.Fatalf("majorOf(%q) should fail", bad)
		}
	}
}

func TestNeedsMajorConfirm(t *testing.T) {
	if !needsMajorConfirm("v1.4.0", "v2.0.0") {
		t.Fatal("1.x -> 2.0 must require confirmation")
	}
	if !needsMajorConfirm("v1.9.9", "v2.0.0-rc.1") {
		t.Fatal("pre-release of next major must require confirmation")
	}
	if needsMajorConfirm("v1.4.0", "v1.5.0") {
		t.Fatal("minor bump must not require confirmation")
	}
	if needsMajorConfirm("v2.0.0", "v1.9.0") {
		t.Fatal("downgrade must not require confirmation")
	}
	if needsMajorConfirm("v1.0.0", "v2026.11.05") {
		t.Fatal("calendar versions must not require confirmation")
	}
	if needsMajorConfirm("", "v2.0.0") || needsMajorConfirm("v1.0.0", "") {
		t.Fatal("unknown versions must not require confirmation")
	}
}
