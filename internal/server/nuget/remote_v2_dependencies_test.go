package nuget_test

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnitRemoteV2EmptyFrameworkDependencyGroups(t *testing.T) {
	for _, dependencies := range []string{
		"::net20|::net35|::net40|::net45|Microsoft.CSharp:[4.3.0, ):netstandard1.0|::net6.0|::netstandard2.0",
		"::portable-net45+win8+wp8+wpa81|::",
		"Dependency:[1.0.0,2.0.0)|::net8.0",
	} {
		t.Run(dependencies, func(t *testing.T) {
			f := newLegacyFixture(t)
			entry := f.legacyPackage("Acme.Mixed", "1.0.0")
			entry = strings.Replace(
				entry,
				"</m:properties>",
				"<d:Dependencies>"+xmlEsc(dependencies)+"</d:Dependencies></m:properties>",
				1,
			)
			f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(entry, ""))
			feed := f.do("GET", remoteV2+"FindPackagesById()?id=%27Acme.Mixed%27", "reader")
			wantStatus(t, feed, 200)
			if !strings.Contains(
				feed.Body.String(),
				"<d:Dependencies>"+xmlEsc(dependencies)+"</d:Dependencies>",
			) {
				t.Fatal("empty dependency groups were not preserved", feed.Body.String())
			}
			path := remoteV2 + "Download/Acme.Mixed/1.0.0"
			for range 2 {
				result := f.do("GET", path, "reader")
				wantStatus(t, result, 200)
				if !bytes.Equal(result.Body.Bytes(), nupkg("Acme.Mixed", "1.0.0", "")) {
					t.Fatal("package bytes differ")
				}
			}
			if f.up.Count("GET", "/binary/Acme.Mixed-1.0.0.zip") != 1 {
				t.Fatal("warm package was not reused")
			}
		})
	}
}

func TestUnitRemoteV2EmptyDependencyGroupAdmissionAndSemVer2(t *testing.T) {
	for _, tc := range []struct {
		dependencies string
		status       int
		visible      bool
	}{
		{"::net8.0|Dependency:[1.0.0-beta.1,2):net8.0", 200, false},
		{"Dependency:[1.0.0-beta.1,2):net8.0|::", 200, false},
		{":1.0.0:net8.0", 502, false},
		{":net8.0", 502, false},
		{"::net8.0:extra", 502, false},
		{"::net8.0|bad/id:1.0.0:net8.0", 502, false},
		{"::net8.0|malformed", 502, false},
	} {
		t.Run(tc.dependencies, func(t *testing.T) {
			f := newLegacyFixture(t)
			entry := f.legacyPackage("Acme.Mixed", "1.0.0")
			entry = strings.Replace(
				entry,
				"</m:properties>",
				"<d:Dependencies>"+xmlEsc(tc.dependencies)+"</d:Dependencies></m:properties>",
				1,
			)
			f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(entry, ""))
			path := remoteV2 + "FindPackagesById()?id=%27Acme.Mixed%27"
			result := f.do("GET", path, "reader")
			wantStatus(t, result, tc.status)
			if strings.Contains(result.Body.String(), "<entry") != tc.visible {
				t.Fatal("legacy client saw an invalid or SemVer2 dependency")
			}
			if tc.status == 200 {
				result = f.do("GET", path+"&semVerLevel=2.0.0", "reader")
				wantStatus(t, result, 200)
				if !strings.Contains(result.Body.String(), "<entry") {
					t.Fatal("SemVer2 client lost a valid package")
				}
			}
		})
	}
}
