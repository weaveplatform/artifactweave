package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestUnitInventoryGuardRejectsUnmappedChanges(t *testing.T) {
	t.Chdir("../..")
	data, err := os.ReadFile("docs/implementation/package-format-inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline catalog
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	if err := validate(baseline); err != nil {
		t.Fatalf("committed catalog: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*catalog)
		want   string
	}{
		{"removed type", func(c *catalog) { c.Rows = c.Rows[1:] }, "registered types"},
		{"changed class", func(c *catalog) { c.Rows[0].Classes = "R" }, "class drift"},
		{"removed route", func(c *catalog) { c.Rows[0].Route = "" }, "unmapped"},
		{"removed owner", func(c *catalog) {
			for i := range c.Rows {
				// Native completion still requires an issue owner. The catalog
				// need not retain an unfinished gap for this guard to be tested.
				if c.Rows[i].P9c {
					c.Rows[i].OwnerIssue = 0
					break
				}
			}
		}, "must have an owning issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed catalog
			if err := json.Unmarshal(data, &changed); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&changed)
			if err := validate(changed); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate error = %v, want %q", err, tc.want)
			}
		})
	}
}
