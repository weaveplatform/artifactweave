package cran

import "testing"

func TestUnitLegacyArchiveTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ from, to string }{
		{"src/contrib/Archive/demo/1.0/demo_1.0.tar.gz", "src/contrib/Archive/demo/demo_1.0.tar.gz"},
		{"src/contrib/Archive/demo/1.0-2/demo_1.0-2.tar.xz", "src/contrib/Archive/demo/demo_1.0-2.tar.xz"},
		{"src/contrib/Archive/demo/1.0/demo_1.0.tar.bz2", "src/contrib/Archive/demo/demo_1.0.tar.bz2"},
		{"src/contrib/demo_1.0.tar.gz", ""},
		{"src/contrib/Archive/demo/demo_1.0.tar.gz", ""},
		{"src/contrib/Archive/demo/1.0/other_1.0.tar.gz", ""},
		{"src/contrib/Archive/demo/1.0/demo_2.0.tar.gz", ""},
		{"src/contrib/Archive/demo/../demo_1.0.tar.gz", ""},
		{"src/contrib/Archive/demo/1.0/demo_1.0.zip", ""},
		{"src/contrib/Archive/demo/1.0/extra/demo_1.0.tar.gz", ""},
		{"bin/windows/contrib/4.6/Archive/demo/1.0/demo_1.0.zip", ""},
	} {
		t.Run(tc.from, func(t *testing.T) {
			got, ok := LegacyArchiveTarget(tc.from)
			if got != tc.to || ok != (tc.to != "") {
				t.Fatalf("got %q, %v; want %q", got, ok, tc.to)
			}
		})
	}
}
