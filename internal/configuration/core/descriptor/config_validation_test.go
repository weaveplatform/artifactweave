package descriptor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

func TestUnitDescriptorConfigFieldTypes(t *testing.T) {
	for _, tc := range invalidConfigFields() {
		t.Run(tc.name, func(t *testing.T) {
			db := inmem.New()
			mustApply(t, db, full)
			before := storeState(t, db)
			for _, dryRun := range []bool{true, false} {
				p, err := Parse([]byte(tc.doc), noEnv)
				if err != nil {
					t.Fatal(err)
				}
				_, err = Apply(t.Context(), depsOf(t, db), p, now, dryRun)
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.field) {
					t.Errorf("dryRun=%t: expected invalid %s, got %v", dryRun, tc.field, err)
				}
				if after := storeState(t, db); after != before {
					t.Fatal("invalid configuration changed persisted state")
				}
			}
		})
	}
}

type invalidConfigField struct{ name, doc, field string }

func invalidConfigFields() []invalidConfigField {
	var cases []invalidConfigField
	for _, field := range []string{"username", "domain", "ntHost", "redirectedToHosts", "services", "platformDefault"} {
		values := []string{"{unexpected: true}", "[unexpected]", "123", "false"}
		if field == "platformDefault" {
			values[len(values)-1] = "'false'"
		}
		for _, value := range values {
			cases = append(cases, invalidConfigField{
				"proxy/" + field + "/" + value,
				fmt.Sprintf("proxies:\n  broken:\n    host: proxy.invalid\n    port: 3128\n    %s: %s\n", field, value), field,
			})
		}
	}
	for _, field := range []string{"multipleChoice", "closedPredefinedValues"} {
		for _, value := range []string{"{unexpected: true}", "[true]", "'false'", "1"} {
			cases = append(cases, invalidConfigField{
				"property/" + field + "/" + value,
				fmt.Sprintf("propertySets:\n  broken:\n    properties:\n      release:\n        %s: %s\n", field, value), field,
			})
		}
	}
	for _, tc := range []struct{ name, fields, field string }{
		{"visible object", "visible: {unexpected: true}", "visible"},
		{"visible string", "visible: 'false'", "visible"},
		{"unknown property field", "properties: {release: {surprise: true}}", "surprise"},
		{"predefined scalar", "properties: {release: {predefinedValues: true}}", "predefinedValues"},
		{"predefined map scalar", "properties: {release: {predefinedValues: {ready: true}}}", "ready"},
		{"predefined map boolean object", "properties: {release: {predefinedValues: {ready: {defaultValue: {unexpected: true}}}}}", "defaultValue"},
		{"predefined map boolean string", "properties: {release: {predefinedValues: {ready: {defaultValue: 'false'}}}}", "defaultValue"},
		{"predefined map unknown", "properties: {release: {predefinedValues: {ready: {surprise: true}}}}", "surprise"},
		{"predefined list boolean", "properties: {release: {predefinedValues: [true]}}", "predefinedValues"},
		{"predefined list null", "properties: {release: {predefinedValues: [null]}}", "predefinedValues"},
		{"predefined list value object", "properties: {release: {predefinedValues: [{value: {unexpected: true}}]}}", "value"},
		{"predefined list default object", "properties: {release: {predefinedValues: [{value: ready, defaultValue: {unexpected: true}}]}}", "defaultValue"},
		{"predefined list unknown", "properties: {release: {predefinedValues: [{value: ready, surprise: true}]}}", "surprise"},
	} {
		cases = append(cases, invalidConfigField{tc.name, "propertySets:\n  broken:\n    " + tc.fields + "\n", tc.field})
	}
	return cases
}

func TestUnitDescriptorConfigValidFormsAndClears(t *testing.T) {
	db := inmem.New()
	for _, doc := range []string{
		`proxies:
  corp:
    host: proxy.invalid
    port: 3128.0
    username: user
    domain: domain
    ntHost: nt
    redirectedToHosts: "one.invalid,two.invalid"
    services: "jfrt,jfxr"
    platformDefault: true
    password: ${PROXY_PASSWORD}
propertySets:
  build:
    visible: true
    properties:
      free: {closedPredefinedValues: false, multipleChoice: false}
      release:
        closedPredefinedValues: true
        multipleChoice: true
        predefinedValues: {ready: {defaultValue: true}, waiting: {defaultValue: false}}
`,
		`proxies:
  corp:
    platformDefault: false
propertySets:
  build:
    visible: false
    properties:
      release:
        closedPredefinedValues: false
        multipleChoice: false
        predefinedValues: [ready, {value: waiting, defaultValue: false}]
`,
		`proxies:
  corp:
    username: null
    domain: null
    ntHost: null
    redirectedToHosts: null
    services: null
    platformDefault: null
    password: null
propertySets:
  build:
    visible: null
    properties:
      removed: null
      cleared: {predefinedValues: null}
      release:
        closedPredefinedValues: null
        multipleChoice: null
        predefinedValues: {ready: null, waiting: {defaultValue: null}}
`,
		"propertySets:\n  build:\n    properties: null\n",
		"proxies:\n  corp: null\npropertySets:\n  build: null\n",
	} {
		mustApply(t, db, doc)
		before := storeState(t, db)
		res := mustApply(t, db, doc)
		if len(res.Created)+len(res.Updated)+len(res.Deleted) != 0 || storeState(t, db) != before {
			t.Fatal("repeated valid descriptor must be unchanged", res)
		}
	}
	if _, err := db.Configs().Get(t.Context(), configdoc.ConfigProxy, "corp"); err == nil {
		t.Fatal("null entry did not delete proxy")
	}
}
