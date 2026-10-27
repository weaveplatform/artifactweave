package jetbrainsplugins

import (
	"bytes"
	"context"
	"strings"
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

func TestUnitMetadataRejectsDeclaredSizeMismatchAndOversizedBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
	}{
		{"negative", -1},
		{"over limit", maxMetadataBytes + 1},
		{"truncated", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := metadataBytes(bytes.NewReader([]byte("[]")), tc.size); err == nil {
				t.Fatalf("metadata size %d accepted", tc.size)
			}
		})
	}
	if err := validateCompatible("com.weave.fixture")(
		context.Background(), bytes.NewReader([]byte("[]")), 3,
	); err == nil {
		t.Fatal("compatible update with truncated body accepted")
	}
	if err := validateSearch(context.Background(), bytes.NewReader([]byte("[]")), 3); err == nil {
		t.Fatal("search with truncated body accepted")
	}
	if err := validatePluginIDs(
		context.Background(),
		bytes.NewReader([]byte("[]")),
		3,
	); err == nil {
		t.Fatal("plugin ID catalog with truncated body accepted")
	}
	if err := validateUpdateMetadata("12345")(
		context.Background(), bytes.NewReader([]byte("{}")), 3,
	); err == nil {
		t.Fatal("update metadata with truncated body accepted")
	}
}

func TestUnitSearchRejectsMalformedAndUntrustedPluginMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"valid", `[{"id":98765,"xmlId":"com.weave.fixture","updateId":12345,"name":"Weave Fixture"}]`, true},
		{"empty", `[]`, true},
		{"null", `null`, false},
		{"malformed", `{`, false},
		{"zero update id", `[{"id":98765,"xmlId":"com.weave.fixture","updateId":0,"name":"Weave Fixture"}]`, false},
		{"control in plugin id", `[{"id":98765,"xmlId":"bad\u000a","updateId":12345,"name":"Weave Fixture"}]`, false},
		{"empty name", `[{"id":98765,"xmlId":"com.weave.fixture","updateId":12345,"name":""}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			err := validateSearch(context.Background(), bytes.NewReader(body), int64(len(body)))
			if (err == nil) != tc.ok {
				t.Fatalf("search metadata error %v, want valid %v", err, tc.ok)
			}
		})
	}
}

func TestUnitPluginIDCatalogRejectsMalformedAndUnsafeIdentifiers(t *testing.T) {
	for _, body := range []string{`null`, `{"ids":[]}`, `["bad\u000a"]`} {
		if err := validatePluginIDs(
			context.Background(), bytes.NewReader([]byte(body)), int64(len(body)),
		); err == nil {
			t.Errorf("unsafe plugin catalog %s accepted", body)
		}
	}
}

func TestUnitBuildWildcardRejectsUnboundedOrUnsafeValues(t *testing.T) {
	for _, build := range []string{strings.Repeat("x", 65), "252/../*", ""} {
		if safeBuildWildcard(build) {
			t.Errorf("unsafe build wildcard %q accepted", build)
		}
	}
}
