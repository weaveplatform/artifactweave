package descriptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// providerLayout is RepositoryLayoutAPIModel from the artifactory_repository_layout
// resource (terraform-provider-artifactory 12.11.14,
// pkg/artifactory/resource/configuration/resource_artifactory_repository_layout.go),
// with its yaml tags and field order, so marshalling it as the resource does
// gives the exact body it sends.
type providerLayout struct {
	Name                             string `yaml:"name"`
	ArtifactPathPattern              string `yaml:"artifactPathPattern"`
	DescriptorPathPattern            string `yaml:"descriptorPathPattern"`
	DistinctiveDescriptorPathPattern bool   `yaml:"distinctiveDescriptorPathPattern"`
	FileIntegrationRevisionRegExp    string `yaml:"fileIntegrationRevisionRegExp"`
	FolderIntegrationRevisionRegExp  string `yaml:"folderIntegrationRevisionRegExp"`
}

// providerLayoutBody is the body of the resource's Create and Update.
func providerLayoutBody(t *testing.T, l providerLayout) string {
	t.Helper()
	body, err := yaml.Marshal(map[string]map[string]any{"repoLayouts": {l.Name: l}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// providerLayoutDelete is the body of the resource's Delete, verbatim.
func providerLayoutDelete(name string) string {
	return fmt.Sprintf(`
repoLayouts:
  %s: ~
`, name)
}

// The resource's acceptance test values (TestAccRepositoryLayout_full).
const (
	tfArtifactPattern   = "[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]"
	tfDescriptorPattern = "[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).pom"
)

func storedLayout(t *testing.T, db *inmem.Store, name string) map[string]any {
	t.Helper()
	doc, err := db.Configs().Get(context.Background(), types.ConfigLayout, name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc.Document, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestUnitRepositoryLayoutResourceCycle applies the bodies the
// artifactory_repository_layout resource sends on create, update and delete.
func TestUnitRepositoryLayoutResourceCycle(t *testing.T) {
	db := inmem.New()
	created := providerLayout{
		Name:                            "tf-layout",
		ArtifactPathPattern:             tfArtifactPattern,
		FolderIntegrationRevisionRegExp: "SNAPSHOT",
		FileIntegrationRevisionRegExp:   "SNAPSHOT|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))",
	}
	body := providerLayoutBody(t, created)
	if !strings.Contains(body, "name: tf-layout") {
		t.Fatalf("the body does not carry the name the resource sends:\n%s", body)
	}
	if res := mustApply(t, db, body); strings.Join(res.Created, " ") != "repoLayouts.tf-layout" {
		t.Fatalf("create %+v", res)
	}
	stored := storedLayout(t, db, "tf-layout")
	if _, ok := stored["name"]; ok || stored["artifactPathPattern"] != tfArtifactPattern ||
		stored["folderIntegrationRevisionRegExp"] != "SNAPSHOT" {
		t.Fatalf("stored %v: the name is the key, not a field", stored)
	}

	// The same body again, and the same layout written without its name,
	// are no change.
	if res := mustApply(t, db, body); strings.Join(res.Unchanged, " ") != "repoLayouts.tf-layout" {
		t.Fatalf("reapply %+v", res)
	}
	var lines []string
	for line := range strings.Lines(body) {
		if strings.TrimSpace(line) != "name: tf-layout" {
			lines = append(lines, line)
		}
	}
	if res := mustApply(t, db, strings.Join(lines, "")); len(res.Unchanged) != 1 {
		t.Fatalf("without the name %+v", res)
	}

	updated := created
	updated.DistinctiveDescriptorPathPattern = true
	updated.DescriptorPathPattern = tfDescriptorPattern
	updated.FolderIntegrationRevisionRegExp = "Foo"
	updated.FileIntegrationRevisionRegExp = "Foo|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))"
	res := mustApply(t, db, providerLayoutBody(t, updated))
	if strings.Join(res.Updated, " ") != "repoLayouts.tf-layout" {
		t.Fatalf("update %+v", res)
	}
	if stored := storedLayout(
		t,
		db,
		"tf-layout",
	); stored["descriptorPathPattern"] != tfDescriptorPattern ||
		stored["distinctiveDescriptorPathPattern"] != true {
		t.Fatalf("updated %v", stored)
	}

	res = mustApply(t, db, providerLayoutDelete("tf-layout"))
	if strings.Join(res.Deleted, " ") != "repoLayouts.tf-layout" {
		t.Fatalf("delete %+v", res)
	}
	_, err := db.Configs().Get(context.Background(), types.ConfigLayout, "tf-layout")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestUnitLayoutOwnName(t *testing.T) {
	for name, c := range map[string]struct {
		doc     string
		message string
	}{
		"matching": {doc: "repoLayouts:\n  l:\n    name: l\n    artifactPathPattern: x\n"},
		"null":     {doc: "repoLayouts:\n  l:\n    name: ~\n    artifactPathPattern: x\n"},
		"different": {
			doc:     "repoLayouts:\n  l:\n    name: other\n    artifactPathPattern: x\n",
			message: "repoLayouts.l: name other does not match the entry key",
		},
		"different case": {
			doc:     "repoLayouts:\n  l:\n    name: L\n    artifactPathPattern: x\n",
			message: "repoLayouts.l: name L does not match the entry key",
		},
		"not a string": {
			doc:     "repoLayouts:\n  \"1\":\n    name: 1\n    artifactPathPattern: x\n",
			message: "repoLayouts.1: name 1 does not match the entry key",
		},
		// Only a layout names itself; elsewhere name stays an unknown field.
		"proxy": {
			doc:     "proxies:\n  p:\n    name: p\n    host: h\n",
			message: "proxies.p: unknown field name",
		},
		"property set": {
			doc:     "propertySets:\n  s:\n    name: s\n",
			message: "propertySets.s: unknown field name",
		},
	} {
		p, err := Parse([]byte(c.doc), noEnv)
		if c.message == "" {
			if err != nil || len(p.Configs) != 1 {
				t.Errorf("%s: %+v %v", name, p, err)
				continue
			}
			if _, ok := p.Configs[0].Fields["name"]; ok {
				t.Errorf("%s: the name was kept as a field: %v", name, p.Configs[0].Fields)
			}
			continue
		}
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.message) {
			t.Errorf("%s: %v, want %q", name, err, c.message)
		}
	}
}
