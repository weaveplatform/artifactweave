package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSelectionUsesReviewedOwnershipAndCommonSmoke(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"common_test.go": `package server
func TestCommon(t *testing.T) {}
func TestMain(m *testing.M) {}
`,
		"maven_test.go": `package server
func TestMaven(t *testing.T) {}
func TestMavenOther(t *testing.T) {}
`,
		"npm_test.go": `package server
func TestNPM(t *testing.T) {}
`,
		"shared_test.go": `package server
func TestShared(t *testing.T) {}
`,
	}
	owners := map[string]string{
		"common_test.go": "common", "maven_test.go": "maven",
		"npm_test.go": "npm", "shared_test.go": "shared",
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := json.Marshal(owners)
	if err != nil {
		t.Fatal(err)
	}
	pattern, selected, total, err := selectTests(root, "maven", inventory)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 3 || total != 5 {
		t.Fatalf("selected %d of %d tests", selected, total)
	}
	re := regexp.MustCompile(pattern)
	for _, name := range []string{"TestCommon", "TestMaven", "TestMavenOther"} {
		if !re.MatchString(name) {
			t.Errorf("missing %s", name)
		}
	}
	for _, name := range []string{"TestNPM", "TestMain", "TestShared", "TestMavenOtherExtra"} {
		if re.MatchString(name) {
			t.Errorf("unrelated %s selected", name)
		}
	}
	if _, _, _, err := selectTests(root, "unmapped", inventory); err == nil {
		t.Fatal("unknown family silently selected tests")
	}
	if err := os.WriteFile(
		filepath.Join(root, "new_test.go"),
		[]byte("package server\nfunc TestNew() {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := selectTests(root, "maven", inventory); err == nil {
		t.Fatal("unclassified file silently omitted")
	}
}

func TestRepositoryInventoryIsCompleteAndUseful(t *testing.T) {
	root := "../../internal/server"
	files, err := filepath.Glob(filepath.Join(root, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 100 {
		t.Fatalf("unexpectedly small server test inventory: %d files", len(files))
	}
	var owners map[string]string
	if err := json.Unmarshal(ownershipJSON, &owners); err != nil {
		t.Fatal(err)
	}
	if len(owners) != len(files) {
		t.Fatalf("ownership covers %d of %d files", len(owners), len(files))
	}
	for _, file := range files {
		owner, ok := owners[filepath.Base(file)]
		if !ok || owner != "common" && owner != "shared" && !families[owner] {
			t.Errorf("unclassified root test %s", file)
		}
	}
	for family := range families {
		pattern, selected, total, err := selectTests(root, family, ownershipJSON)
		if err != nil || selected == 0 || total < 300 || !strings.HasPrefix(pattern, "^(") {
			t.Fatalf("%s: %d/%d: %v", family, selected, total, err)
		}
		if selected >= total/2 {
			t.Errorf("%s partition too broad: %d of %d tests", family, selected, total)
		}
	}
}
