package machinelearning_test

import (
	"strings"
	"testing"

	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
)

func TestUnitModelNamesVersionsAndFilePaths(t *testing.T) {
	for _, s := range []string{"model", "group/model", "A_Z-1.2"} {
		if !ml.Name(s) {
			t.Errorf("valid name %q", s)
		}
	}
	for _, s := range []string{"", "a/b/c", "tmp", "a/tmp", "../model", "name space", "ümlaut", strings.Repeat("a", 61)} {
		if ml.Name(s) {
			t.Errorf("invalid name %q accepted", s)
		}
	}
	for _, s := range []string{"weights.bin", "nested/模型.pkl", "has space/model.py", ".config"} {
		if !ml.File(s) {
			t.Errorf("valid file %q", s)
		}
	}
	for _, s := range []string{"", "/root", "a/", "../a", "a/./b", "a//b", "a\\b", "a?b", "a#b", "a%b", "a;b", "a\x00b", "a\nb", string([]byte{0xff}), strings.Repeat("a", 256), strings.Repeat("abc/", 300), "weights!/model.pkl"} {
		if ml.File(s) {
			t.Errorf("invalid file %q accepted", s)
		}
	}
	for _, s := range []string{"1793180000000", "9999999999999"} {
		if !ml.Transaction(s) {
			t.Errorf("valid transaction %s", s)
		}
	}
	for _, s := range []string{"", "1", "0000000000000", "0179318000000", "-793180000000", "1abcdefghijk1"} {
		if ml.Transaction(s) {
			t.Errorf("invalid transaction %q", s)
		}
	}
	for _, s := range []string{"1.2.3", "release_1", "2026-10-28"} {
		if !ml.Version(s) {
			t.Errorf("valid version %q", s)
		}
	}
	for _, s := range []string{"tmp", "files", "dependencies", "code", "..", ""} {
		if ml.Version(s) {
			t.Errorf("reserved or invalid version %q", s)
		}
	}
}

func TestUnitModelLayoutRoundTripsWithoutNamespaceCategoryAmbiguity(t *testing.T) {
	for _, id := range []ml.Identity{{Name: "model", Version: "1.2.3"}, {Name: "group/model", Version: "release-1"}} {
		for _, tc := range []struct {
			path, tx, rest string
			private        bool
		}{
			{id.Manifest(), "", ml.ManifestName, true},
			{id.Prefix() + "/files/weights/model.bin", "", "files/weights/model.bin", false},
			{id.Prefix() + "/dependencies/python.json", "", "dependencies/python.json", false},
			{id.Prefix() + "/code/entry.py", "", "code/entry.py", false},
			{id.Staging("1793180000000") + "/" + ml.TransactionName, "1793180000000", ml.TransactionName, true},
			{id.Staging("1793180000000") + "/files/weights.bin", "1793180000000", "files/weights.bin", true},
		} {
			got, tx, rest, ok := ml.ParsePath(tc.path)
			if !ok || got != id || tx != tc.tx || rest != tc.rest ||
				ml.Private(tc.path) != tc.private {
				t.Errorf("parse %s: %+v %q %q %v", tc.path, got, tx, rest, ok)
			}
		}
	}
	for _, path := range []string{"", "other/model/1/files/x", "models/a/1", "models/a/1/unknown/x", "models/a/tmp/1", "models/a/tmp/1/bad/files/x", "models/a/1/files/../secret", "models/a/tmp/1/1793180000000/model-manifest.json", "models/a/1/.transaction.json", "models/a/tmp/tmp/1793180000000/files/x"} {
		if _, _, _, ok := ml.ParsePath(path); ok {
			t.Errorf("invalid model path %q accepted", path)
		}
	}
	// These version words occupy the file-category position for a shorter
	// one-segment model name. Rejecting them prevents a namespaced manifest
	// becoming a publicly downloadable artifact of a different model.
	for _, version := range []string{"files", "dependencies", "code", "tmp"} {
		if ml.Version(version) {
			t.Errorf("ambiguous namespaced version %s accepted", version)
		}
	}
}
