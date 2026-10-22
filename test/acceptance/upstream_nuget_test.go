package acceptance

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// NuGet.org v2 answers GET metadata requests but returns 404 for HEAD on
// FindPackagesById. Use the existing repository option only for that fixture.
func configureUpstreamNuGet(config map[string]any, family string) {
	config["packageType"] = "nuget"
	config["url"] = "https://api.nuget.org/v3/index.json"
	config["v3FeedUrl"] = "https://api.nuget.org/v3/index.json"
	if family == "nuget-v2" {
		// Defaults append api/v2 and api/v2/package to the configured origin.
		config["url"] = "https://www.nuget.org"
		config["bypassHeadRequests"] = true
		delete(config, "v3FeedUrl")
	}
	config["externalDependenciesEnabled"] = true
	config["externalDependenciesPatterns"] = []string{
		"https://api.nuget.org/**",
		"https://*.nuget.org/**",
	}
}

func TestUpstreamConformanceNuGetRequestConfiguration(t *testing.T) {
	v2 := map[string]any{}
	configureUpstreamNuGet(v2, "nuget-v2")
	if v2["url"] != "https://www.nuget.org" || v2["bypassHeadRequests"] != true {
		t.Fatal("v2 must use the origin with default contexts and direct GET metadata")
	}
	if _, ok := v2["v3FeedUrl"]; ok {
		t.Fatal("v2 must not select the v3 discovery surface")
	}
	v3 := map[string]any{}
	configureUpstreamNuGet(v3, "nuget-v3")
	if v3["url"] != "https://api.nuget.org/v3/index.json" || v3["v3FeedUrl"] != v3["url"] {
		t.Fatal("v3 must keep service-index discovery")
	}
	if _, ok := v3["bypassHeadRequests"]; ok {
		t.Fatal("the observed v2 HEAD exception must not change the v3 policy")
	}
}

func upstreamNuGetVersion(data []byte, version string) bool {
	var doc struct {
		Versions []string `json:"versions"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	for _, candidate := range doc.Versions {
		if candidate == version {
			return true
		}
	}
	return false
}

// A matching title elsewhere in the feed is not package discovery. Match the
// exact protocol identity/version and follow only its rewritten content URL.
func upstreamNuGetV2Package(origin string, data []byte, id, version string) (string, error) {
	var feed struct {
		XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
		Entries []struct {
			Content []struct {
				Source string `xml:"src,attr"`
			} `xml:"http://www.w3.org/2005/Atom content"`
			Properties struct {
				ID      string `xml:"http://schemas.microsoft.com/ado/2007/08/dataservices Id"`
				Version string `xml:"http://schemas.microsoft.com/ado/2007/08/dataservices Version"`
			} `xml:"http://schemas.microsoft.com/ado/2007/08/dataservices/metadata properties"`
		} `xml:"http://www.w3.org/2005/Atom entry"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&feed) != nil {
		return "", fmt.Errorf("invalid NuGet Atom feed")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return "", fmt.Errorf("trailing NuGet Atom document")
	}
	source := ""
	found := false
	for _, entry := range feed.Entries {
		if !strings.EqualFold(entry.Properties.ID, id) || entry.Properties.Version != version {
			continue
		}
		if found || len(entry.Content) != 1 || entry.Content[0].Source == "" {
			return "", fmt.Errorf("ambiguous or missing pinned package source")
		}
		source = entry.Content[0].Source
		found = true
	}
	if !found {
		return "", fmt.Errorf("pinned package identity/version absent from Atom feed")
	}
	return localUpstreamLink(origin, source)
}

func TestUpstreamConformanceNuGetExactDiscovery(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"versions":["13.0.2","13.0.3"]}`, true},
		{`{"versions":["113.0.3","13.0.30"]}`, false},
		{`{"versions":[],"description":"13.0.3"}`, false},
		{`{"versions":["13.0.3"]} trailing`, false},
		{`{"versions":"13.0.3"}`, false},
	} {
		if got := upstreamNuGetVersion([]byte(tc.body), "13.0.3"); got != tc.want {
			t.Fatalf("exact version membership %s: %v", tc.body, got)
		}
	}
	const start = `<feed xmlns="http://www.w3.org/2005/Atom" xmlns:m="http://schemas.microsoft.com/ado/2007/08/dataservices/metadata" xmlns:d="http://schemas.microsoft.com/ado/2007/08/dataservices">`
	const entry = `<entry><title>Newtonsoft.Json 13.0.3</title><content type="application/zip" src="/rewritten/pinned.nupkg"/><m:properties><d:Id>Newtonsoft.Json</d:Id><d:Version>13.0.3</d:Version></m:properties></entry>`
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"exact", start + entry + `</feed>`, true},
		{"case-insensitive-id", start + strings.ReplaceAll(entry, ">Newtonsoft.Json<", ">newtonsoft.json<") + `</feed>`, true},
		{"wrong-version", start + strings.ReplaceAll(entry, "<d:Version>13.0.3", "<d:Version>13.0.30") + `</feed>`, false},
		{"wrong-id", start + strings.ReplaceAll(entry, "<d:Id>Newtonsoft.Json", "<d:Id>Other.Package") + `</feed>`, false},
		{"no-source", start + strings.ReplaceAll(entry, ` src="/rewritten/pinned.nupkg"`, "") + `</feed>`, false},
		{"external-source", start + strings.ReplaceAll(entry, "/rewritten/pinned.nupkg", "https://upstream.example/package") + `</feed>`, false},
		{"duplicate", start + entry + entry + `</feed>`, false},
		{"duplicate-content", start + strings.Replace(entry, "<m:properties>", `<content src="/wrong.nupkg"/><m:properties>`, 1) + `</feed>`, false},
		{"wrong-namespace", strings.ReplaceAll(start+entry+`</feed>`, "http://www.w3.org/2005/Atom", "urn:other"), false},
		{"trailing", start + entry + `</feed><feed/>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := upstreamNuGetV2Package(
				"http://localhost:123",
				[]byte(tc.body),
				"Newtonsoft.Json",
				"13.0.3",
			)
			if (err == nil) != tc.want {
				t.Fatal("exact Atom discovery", target, err)
			}
			if tc.want && target != "http://localhost:123/rewritten/pinned.nupkg" {
				t.Fatal("did not follow rewritten package source", target)
			}
		})
	}
}
