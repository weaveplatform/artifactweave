package types

import (
	"testing"
	"time"
)

func TestUnitEncodeBuildInfoSegment(t *testing.T) {
	// Expected values follow jfrog-client-go's encodeForBuildInfoRepository,
	// including its %22 for an apostrophe.
	for in, want := range map[string]string{
		"app":              "app",
		"team/app":         "team%2Fapp",
		`a\b|c*d?e`:        "a%5Cb%7Cc%2Ad%3Fe",
		"it's":             "it%22s",
		"a:b;c%d":          "a%3Ab%3Bc%25d",
		"a / b":            "a%20%2F%20b",
		"a\t/\nb":          "a%20%2F%20b",
		"two  words":       "two  words",
		"x /y":             "x%20%2Fy",
		"x/ y":             "x%2F%20y",
		"1.2.3-rc+build.4": "1.2.3-rc+build.4",
	} {
		if got := EncodeBuildInfoSegment(in); got != want {
			t.Errorf("EncodeBuildInfoSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnitBuildInfoRepo(t *testing.T) {
	for in, want := range map[string]string{
		"":        "artifactory-build-info",
		"default": "artifactory-build-info",
		"proj":    "proj-build-info",
		"a:b":     "a%3Ab-build-info",
	} {
		if got := BuildInfoRepo(in); got != want {
			t.Errorf("BuildInfoRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnitBuildDocumentItem(t *testing.T) {
	started := time.Date(2026, 10, 15, 9, 30, 0, 123_456_789, time.UTC)
	b := Build{ProjectKey: "proj", Name: "team/app", Number: "1.0 / rc", Started: started}
	repo, full := b.DocumentItem()
	if repo != "proj-build-info" {
		t.Errorf("repo = %q", repo)
	}
	if want := "team%2Fapp/1.0%20%2F%20rc-1792056600123.json"; full != want {
		t.Errorf("path = %q, want %q", full, want)
	}
}

func TestUnitBuildRun(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	b := Build{
		ID: 7, ProjectKey: "p", Name: "n", Number: "3", Started: at, Created: at.Add(time.Second),
		URL: "https://ci", Document: []byte("{}"),
	}
	want := BuildRun{
		ID:         7,
		ProjectKey: "p",
		Name:       "n",
		Number:     "3",
		Started:    at,
		Created:    at.Add(time.Second),
	}
	if got := b.Run(); got != want {
		t.Fatalf("Run() = %+v, want %+v", got, want)
	}
}
