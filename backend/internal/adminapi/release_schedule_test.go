package adminapi

import "testing"

func TestParseClock(t *testing.T) {
	cases := []struct {
		in     string
		h, m   int
		wantOK bool
	}{
		{"06:00", 6, 0, true},
		{"23:59", 23, 59, true},
		{" 07:30 ", 7, 30, true},
		{"24:00", 0, 0, false},
		{"6", 0, 0, false},
		{"06:60", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		h, m, ok := parseClock(c.in)
		if ok != c.wantOK || (ok && (h != c.h || m != c.m)) {
			t.Fatalf("parseClock(%q) = %d,%d,%v; want %d,%d,%v", c.in, h, m, ok, c.h, c.m, c.wantOK)
		}
	}
}