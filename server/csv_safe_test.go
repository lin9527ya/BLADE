package server

import "testing"

func TestCSVSafeCellBlocksFormulaPrefixes(t *testing.T) {
	for _, in := range []string{"=cmd|' /C calc'!A0", "+SUM(A1)", "-1+2", "@user", "\t=1", "\r=1"} {
		if got := csvSafeCell(in); got != "'"+in {
			t.Fatalf("csvSafeCell(%q)=%q", in, got)
		}
	}
	for _, in := range []string{"", "normal", "C:\\path", "2026-01-01"} {
		if got := csvSafeCell(in); got != in {
			t.Fatalf("safe cell changed %q -> %q", in, got)
		}
	}
}
