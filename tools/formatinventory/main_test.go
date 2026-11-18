package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		{"invalid schema", func(c *catalog) { c.Schema = 0 }, "catalog schema"},
		{"removed type", func(c *catalog) { c.Rows = c.Rows[1:] }, "registered types"},
		{"duplicate type", func(c *catalog) { c.Rows[1] = c.Rows[0] }, "duplicate format"},
		{"unregistered type", func(c *catalog) { c.Rows[0].Type = "unregistered" }, "unregistered inventory"},
		{"changed class", func(c *catalog) { c.Rows[0].Classes = "R" }, "class drift"},
		{"removed route", func(c *catalog) { c.Rows[0].Route = "" }, "unmapped"},
		{"missing route owner", func(c *catalog) { c.Rows[0].RouteFile = "absent.go" }, "route owner"},
		{"invalid primary source", func(c *catalog) { c.Rows[0].Source = "http://example.test" }, "valid versioned source"},
		{"unbacked native claim", func(c *catalog) { c.Rows[0].Evidence = "" }, "claims native behavior"},
		{"absent native evidence", func(c *catalog) { c.Rows[0].Evidence = "absent.feature" }, "evidence"},
		{"unknown implementation status", func(c *catalog) { c.Rows[0].Status = "complete" }, "unknown status"},
		{"unowned planned work", func(c *catalog) {
			c.Rows[0].Status, c.Rows[0].OwnerIssue, c.Rows[0].OwnerPhase, c.Rows[0].P9c = "planned", 0, "", false
		}, "planned type has no owner"},
		{"catalog membership drift", func(c *catalog) { c.Rows[0].Catalog = false }, "expected 45 guide types"},
		{"P9c ownership count drift", func(c *catalog) { c.Rows[0].P9c = !c.Rows[0].P9c }, "expected 45 guide types"},
		{"gap without owner", func(c *catalog) { c.Rows[0].Status, c.Rows[0].P9c, c.Rows[0].OwnerIssue = "gap", false, 0 }, "gap must have an issue"},
		{"gap without class operation", func(c *catalog) { c.Rows[0].Status, c.Rows[0].Evidence, c.Rows[0].Operations = "gap", "", "V: resolve" }, "client-operation matrix"},
		{"missing virtual mechanism", func(c *catalog) { c.Rows[0].Virtual = "" }, "unknown virtual mechanism"},
		{"unknown virtual mechanism", func(c *catalog) { c.Rows[0].Virtual = "ordered" }, "unknown virtual mechanism"},
		{"virtual class mismatch", func(c *catalog) { c.Rows[0].Virtual = "not supported" }, "contradicts"},
		{"missing virtual evidence", func(c *catalog) { c.Rows[0].VirtualEvidence = nil }, "evidence are required"},
		{"missing virtual operations", func(c *catalog) { c.Rows[0].Operations = "L: publish" }, "operations and evidence"},
		{"missing virtual proof file", func(c *catalog) { c.Rows[0].VirtualEvidence[0].Path = "absent.feature" }, "virtual evidence"},
		{"unknown virtual proof kind", func(c *catalog) { c.Rows[0].VirtualEvidence[0].Kind = "unit" }, "unknown evidence kind"},
		{"missing virtual anchor", func(c *catalog) { c.Rows[0].VirtualEvidence[0].Anchor = "" }, "anchor are required"},
		{"wrong virtual scenario", func(c *catalog) { c.Rows[0].VirtualEvidence[0].Anchor = "not a real scenario" }, "missing acceptance"},
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

func TestUnitVirtualEvidenceStrength(t *testing.T) {
	t.Chdir("../..")
	client := virtualProof{
		Kind:   "client",
		Path:   "test/acceptance/features/npm_virtual.feature",
		Anchor: "<npm> installs a cold mixed dependency tree and verifies its virtual lockfile with npm ci",
	}
	server := virtualProof{Kind: "server", Path: "internal/artifacts/ecosystem/bazelmodules/http/bazelmodules_test.go", Anchor: "TestAccBazelSharedVirtualGraph"}
	for _, p := range []virtualProof{client, server} {
		if err := validateVirtualProof(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []string{"missing path", "unsupported file", "unit anchor", "absent test"} {
		t.Run(change, func(t *testing.T) {
			p := server
			switch change {
			case "missing path":
				p.Path = ""
			case "unsupported file":
				p.Path = "README.md"
			case "unit anchor":
				p.Anchor = "TestUnitNotAcceptance"
			case "absent test":
				p.Anchor = "TestAccNotPresent"
			}
			if err := validateVirtualProof(p); err == nil {
				t.Fatal("invalid proof admitted", change)
			}
		})
	}
	r := row{Type: "cargo", Classes: "LRF", Virtual: "not supported"}
	if err := validateVirtual(r); err != nil {
		t.Fatal(err)
	}
	r.VirtualEvidence = []virtualProof{client}
	if err := validateVirtual(r); err == nil {
		t.Fatal("unsupported class claims virtual evidence")
	}
	r.VirtualEvidence = nil
	r.Operations = "V: resolve"
	if err := validateVirtual(r); err == nil {
		t.Fatal("unsupported class claims virtual operations")
	}
}

func TestUnitInventoryGenerationAndDrift(t *testing.T) {
	t.Chdir("../..")
	const source = "docs/implementation/package-format-inventory.json"
	out := filepath.Join(t.TempDir(), "matrix.md")
	if err := run(false, source, out); err != nil {
		t.Fatal(err)
	}
	if err := run(true, source, out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| Virtual resolution |", "native merge", "item routes", "first member wins", "not supported", "[client:", "[server:"} {
		if !strings.Contains(string(data), want) {
			t.Fatal("missing classification/evidence", want)
		}
	}
	if err := os.WriteFile(out, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(true, source, out); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatal("missing drift refusal", err)
	}
	if err := run(true, source, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing output accepted")
	}
	if err := run(false, "absent.json", out); err == nil {
		t.Fatal("missing input accepted")
	}
	bad := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(false, bad, out); err == nil {
		t.Fatal("invalid input accepted")
	}
	if err := os.WriteFile(bad, []byte(`{"schema":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(false, bad, out); err == nil {
		t.Fatal("invalid catalog accepted")
	}
	if err := run(false, source, t.TempDir()); err == nil {
		t.Fatal("output directory accepted as file")
	}
}

func TestUnitVirtualGoEvidenceRequiresADeclaration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proof_test.go")
	p := virtualProof{Kind: "client", Path: path, Anchor: "TestAccProof"}
	for _, body := range []string{
		"package proof\n// func TestAccProof(t *testing.T) {}\n",
		"package proof\nfunc TestAccProof( broken syntax",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateVirtualProof(p); err == nil {
			t.Fatal("non-declaration acquired client evidence")
		}
	}
	if err := os.WriteFile(path, []byte("package proof\nimport \"testing\"\nfunc TestAccProof(t *testing.T) {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateVirtualProof(p); err != nil {
		t.Fatal(err)
	}
}
