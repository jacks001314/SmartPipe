package adminapi

import "testing"

func TestSplitGroupsTrimsAndDropsEmpty(t *testing.T) {
	got := splitGroups(" grp-a, ,grp-b ")
	if len(got) != 2 || got[0] != "grp-a" || got[1] != "grp-b" {
		t.Fatalf("unexpected groups: %#v", got)
	}
}

func TestRemoveGroupKeepsOthers(t *testing.T) {
	got := removeGroup([]string{"a", "b", "c"}, "b")
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("unexpected groups: %#v", got)
	}
}