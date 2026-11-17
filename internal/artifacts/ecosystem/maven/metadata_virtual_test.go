package maven_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func metadataDoc(t *testing.T, body string, modified int) virtual.Part[maven.MetadataDocument] {
	t.Helper()
	d, err := maven.ParseMetadata([]byte(body), time.Unix(int64(modified), 0))
	if err != nil {
		t.Fatal(err)
	}
	return virtual.Part[maven.MetadataDocument]{Doc: d}
}

func TestUnitMavenMetadataMerge(t *testing.T) {
	a := `<metadata modelVersion="1.0.0"><groupId>a&amp;b</groupId><artifactId>lib</artifactId><versioning><latest>old</latest><release>old</release><versions><version>10.0</version><version>2.0</version></versions><lastUpdated>20261117120000</lastUpdated></versioning><plugins><plugin><name>First</name><prefix>p</prefix><artifactId>one</artifactId></plugin></plugins></metadata>`
	b := `<metadata modelVersion="1.1.0"><groupId>wrong</groupId><versioning><versions><version>2.0</version><version>11-SNAPSHOT</version><version>3.0</version></versions><lastUpdated>20261117110000</lastUpdated></versioning><plugins><plugin><prefix>p</prefix><artifactId>wrong</artifactId></plugin><plugin><prefix>q</prefix><artifactId>two</artifactId></plugin></plugins></metadata>`
	parts := []virtual.Part[maven.MetadataDocument]{metadataDoc(t, a, 10), metadataDoc(t, b, 20)}
	d, err := (maven.Merger{}).Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	for _, want := range []string{`modelVersion="1.0.0"`, `<groupId>a&amp;b</groupId>`, `<latest>11-SNAPSHOT</latest>`, `<release>10.0</release>`, "<version>2.0</version>\n      <version>3.0</version>\n      <version>10.0</version>\n      <version>11-SNAPSHOT</version>", `<lastUpdated>20261117120000</lastUpdated>`, `<artifactId>one</artifactId>`, `<artifactId>two</artifactId>`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "wrong") || strings.Count(out, "<version>2.0</version>") != 1 || !d.Modified.Equal(time.Unix(20, 0)) {
		t.Fatal(out, d.Modified)
	}
	if strings.Contains(string(parts[0].Doc.Bytes()), "11-SNAPSHOT") {
		t.Fatal("member mutated")
	}
	single, err := (maven.Merger{}).Merge(t.Context(), parts[:1])
	if err != nil || !strings.Contains(string(single.Bytes()), "<latest>old</latest>") ||
		strings.Index(string(single.Bytes()), "10.0</version>") > strings.Index(string(single.Bytes()), "2.0</version>") {
		t.Fatal("single member sorted", err)
	}
	if _, err := (maven.Merger{}).Merge(t.Context(), nil); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestUnitMavenMetadataMergeSnapshots(t *testing.T) {
	a := `<metadata><version>1-SNAPSHOT</version><versioning><snapshot><timestamp>20261117.120000</timestamp><buildNumber>9</buildNumber><localCopy>true</localCopy></snapshot><lastUpdated>20261117120000</lastUpdated><snapshotVersions><snapshotVersion><extension>jar</extension><value>1-20261117.120000-9</value><updated>newer-time</updated></snapshotVersion></snapshotVersions></versioning></metadata>`
	b := `<metadata><versioning><snapshot><timestamp>20261116.120000</timestamp><buildNumber>10</buildNumber></snapshot><lastUpdated>20261116120000</lastUpdated><snapshotVersions><snapshotVersion><extension>jar</extension><value>1-20261116.120000-10</value><updated>higher-build</updated></snapshotVersion><snapshotVersion><classifier>sources</classifier><extension>jar</extension><value>1-20261116.120000-11</value></snapshotVersion></snapshotVersions></versioning></metadata>`
	for _, enabled := range []bool{true, false} {
		d, err := (maven.Merger{SnapshotVersions: enabled}).Merge(t.Context(), []virtual.Part[maven.MetadataDocument]{metadataDoc(t, a, 20), metadataDoc(t, b, 10)})
		if err != nil {
			t.Fatal(err)
		}
		out := string(d.Bytes())
		if !strings.Contains(out, "<buildNumber>10</buildNumber>") || !strings.Contains(out, "<lastUpdated>20261117120000</lastUpdated>") || strings.Contains(out, "sources") {
			t.Fatal(out)
		}
		if strings.Contains(out, "snapshotVersions") != enabled || enabled && !strings.Contains(out, "higher-build") {
			t.Fatal(out)
		}
	}
	for _, base := range []string{`<metadata/>`, `<metadata><versioning/></metadata>`} {
		d, err := (maven.Merger{SnapshotVersions: true}).Merge(t.Context(), []virtual.Part[maven.MetadataDocument]{metadataDoc(t, base, 30), metadataDoc(t, b, 10)})
		if err != nil || !strings.Contains(string(d.Bytes()), "sources") || !d.Modified.Equal(time.Unix(30, 0)) {
			t.Fatal(d, err)
		}
	}
	// Equal and lower builds never replace a matching entry or snapshot.
	for _, replacement := range []string{"9", "8"} {
		d, err := (maven.Merger{SnapshotVersions: true}).Merge(
			t.Context(),
			[]virtual.Part[maven.MetadataDocument]{metadataDoc(t, a, 20), metadataDoc(t, strings.ReplaceAll(b, "10", replacement), 10)},
		)
		if err != nil || !strings.Contains(string(d.Bytes()), "newer-time") || !strings.Contains(string(d.Bytes()), "<localCopy>true</localCopy>") {
			t.Fatal(d, err)
		}
	}
}

func TestUnitMavenMetadataTimestampMerge(t *testing.T) {
	for _, tc := range []struct{ old, next, want, stamp string }{
		{"20260101", "20260102", "next", "20260102"},
		{"20260101", "20260101", "next", "20260101"},
		{"20260102", "20260101", "old", "20260102"},
		{"null", "20260101", "next", "20260101"},
		{"20260101", "null", "next", "20260101"},
		{"20260101", "", "next", "20260101"},
	} {
		a := `<metadata><versioning><latest>old</latest><release>old</release><lastUpdated>` + tc.old + `</lastUpdated></versioning></metadata>`
		b := `<metadata><versioning><latest>next</latest><release>next</release><lastUpdated>` + tc.next + `</lastUpdated></versioning></metadata>`
		d, err := (maven.Merger{}).Merge(t.Context(), []virtual.Part[maven.MetadataDocument]{metadataDoc(t, a, 0), metadataDoc(t, b, 0)})
		if err != nil || !strings.Contains(string(d.Bytes()), "<latest>"+tc.want+"</latest>") || !strings.Contains(string(d.Bytes()), "<lastUpdated>"+tc.stamp+"</lastUpdated>") {
			t.Fatal(tc, d, err)
		}
	}
}

func TestUnitMavenMetadataParsingAndClientGate(t *testing.T) {
	for _, body := range []string{`<wrong/>`, `<metadata><versioning>`, strings.Repeat("x", 1<<20+1)} {
		if _, err := maven.ParseMetadata([]byte(body), time.Time{}); err == nil {
			t.Fatalf("accepted malformed %q", body[:min(len(body), 40)])
		}
	}
	d := metadataDoc(t, `<metadata><groupId>a & b &amp; c &#38; d &unknown;</groupId></metadata>`, 0)
	if !strings.Contains(string(d.Doc.Bytes()), "a &amp; b &amp; c &amp; d &amp;unknown;") {
		t.Fatal(string(d.Doc.Bytes()))
	}
	for _, tc := range []struct {
		agents []string
		want   bool
	}{
		{nil, true}, {[]string{""}, true}, {[]string{"Apache-Maven/3.9.0"}, true}, {[]string{"Java/17"}, false}, {[]string{"java/1.8"}, false}, {[]string{"Apache Ivy/2.0"}, false}, {[]string{"Maven", "Wharf Ivy/1.0"}, false}, {[]string{"prefix Java/17"}, true},
	} {
		if got := maven.SupportsSnapshotVersions(tc.agents); got != tc.want {
			t.Fatal(tc, got)
		}
	}
	if !maven.SnapshotMetadata("a/1-SNAPSHOT/maven-metadata.xml") || maven.SnapshotMetadata("a/maven-metadata.xml") {
		t.Fatal("snapshot path")
	}
}
