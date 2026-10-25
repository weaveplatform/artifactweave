package jetbrainsplugins

import (
	"bytes"
	"context"
	"testing"
)

func TestUnitCompatibleUpdateRequiresRequestedPluginIdentity(t *testing.T) {
	validate := validateCompatible("com.weave.fixture")
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`[{"id":12345,"pluginId":98765,"version":"1.0.0","pluginXmlId":"com.weave.fixture"}]`, true},
		{`[]`, true},
		{`[{"id":12345,"pluginId":98765,"version":"1.0.0","pluginXmlId":"com.other"}]`, false},
		{`[{"id":0,"pluginId":98765,"version":"1.0.0","pluginXmlId":"com.weave.fixture"}]`, false},
		{`{"id":12345}`, false},
	} {
		err := validate(context.Background(), bytes.NewReader([]byte(tc.body)), int64(len(tc.body)))
		if (err == nil) != tc.ok {
			t.Errorf("body %s: err %v, want valid %v", tc.body, err, tc.ok)
		}
	}
}

func TestUnitUpdateMetadataRejectsMismatchedUpdateAndUnsafeFields(t *testing.T) {
	validate := validateUpdateMetadata("12345")
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{`{"id":12345,"xmlId":"com.weave.fixture","name":"Weave Fixture","version":"1.0.0","since":"252","until":"252.*","size":512}`, true},
		{`{"id":99999,"xmlId":"com.weave.fixture","name":"Weave Fixture","version":"1.0.0","since":"252","size":512}`, false},
		{`{"id":12345,"xmlId":"bad\u000a","name":"Weave Fixture","version":"1.0.0","since":"252","size":512}`, false},
		{`{"id":12345,"xmlId":"com.weave.fixture","name":"Weave Fixture","version":"1.0.0","since":"252","size":1073741825}`, false},
	} {
		err := validate(context.Background(), bytes.NewReader([]byte(tc.body)), int64(len(tc.body)))
		if (err == nil) != tc.ok {
			t.Errorf("body %s: err %v, want valid %v", tc.body, err, tc.ok)
		}
	}
}

func TestUnitPluginIDCatalogAcceptsOfficialLegacyNames(t *testing.T) {
	// These spellings occur in the live Marketplace pluginsXMLIds.json catalog
	// (checked 2026-10-25). They are identifiers, not repository path segments.
	body := []byte(`["Gold Section","RSS/Atom feed reader","com.weave.fixture"]`)
	if err := validatePluginIDs(
		context.Background(),
		bytes.NewReader(body),
		int64(len(body)),
	); err != nil {
		t.Fatalf("valid Marketplace catalog rejected: %v", err)
	}
}
