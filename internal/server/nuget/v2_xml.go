package nuget

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	atomNS     = "http://www.w3.org/2005/Atom"
	dataNS     = "http://schemas.microsoft.com/ado/2007/08/dataservices"
	metadataNS = "http://schemas.microsoft.com/ado/2007/08/dataservices/metadata"
)

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}
type atomContent struct {
	Type string `xml:"type,attr"`
	Src  string `xml:"src,attr"`
}
type atomAuthor struct {
	Name string `xml:"name"`
}
type atomCategory struct {
	Term   string `xml:"term,attr"`
	Scheme string `xml:"scheme,attr"`
}
type atomProperty struct {
	XMLName xml.Name
	Type    string `xml:"m:type,attr,omitempty"`
	Value   string `xml:",chardata"`
}
type atomProperties struct {
	Items []atomProperty `xml:",any"`
}

type atomEntry struct {
	XMLName    xml.Name       `xml:"entry"`
	XMLNS      string         `xml:"xmlns,attr"`
	DataNS     string         `xml:"xmlns:d,attr"`
	MetadataNS string         `xml:"xmlns:m,attr"`
	ID         string         `xml:"id"`
	Title      string         `xml:"title"`
	Summary    string         `xml:"summary"`
	Updated    string         `xml:"updated"`
	Authors    []atomAuthor   `xml:"author"`
	Category   atomCategory   `xml:"category"`
	Content    atomContent    `xml:"content"`
	Properties atomProperties `xml:"m:properties"`
}
type atomFeed struct {
	XMLName xml.Name    `xml:"feed"`
	XMLNS   string      `xml:"xmlns,attr"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

func writeXML(w http.ResponseWriter, contentType string, value any) {
	// These wire models contain only strings and slices; there are no
	// custom marshalers or unsupported values that can fail encoding.
	body, _ := xml.Marshal(value)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("DataServiceVersion", "2.0")
	//nolint:gosec // G705: encoding/xml escapes every dynamic value; the response is XML
	_, _ = w.Write(append([]byte(xml.Header), body...))
}

func writeV2Service(w http.ResponseWriter, r *http.Request) {
	writeXML(w, "application/xml", v2Service(r))
}

func v2Service(r *http.Request) any {
	type collection struct {
		Href  string `xml:"href,attr"`
		Title string `xml:"atom:title"`
	}
	type workspace struct {
		Title      string     `xml:"atom:title"`
		Collection collection `xml:"collection"`
	}
	type service struct {
		XMLName   xml.Name  `xml:"service"`
		XMLNS     string    `xml:"xmlns,attr"`
		AtomNS    string    `xml:"xmlns:atom,attr"`
		Base      string    `xml:"xml:base,attr"`
		Workspace workspace `xml:"workspace"`
	}
	return service{
		XMLNS:  "http://www.w3.org/2007/app",
		AtomNS: atomNS,
		Base:   v2Base(r) + "/",
		Workspace: workspace{
			Title:      "Default",
			Collection: collection{Href: "Packages", Title: "Packages"},
		},
	}
}

// Each property's EDM type is shared with the metadata document so a
// native client's capability and entry parsers cannot disagree.
var v2PropertyTypes = map[string]string{
	"IsPrerelease":             "Edm.Boolean",
	"IsLatestVersion":          "Edm.Boolean",
	"IsAbsoluteLatestVersion":  "Edm.Boolean",
	"Listed":                   "Edm.Boolean",
	"RequireLicenseAcceptance": "Edm.Boolean",
	"Created":                  "Edm.DateTime",
	"LastUpdated":              "Edm.DateTime",
	"Published":                "Edm.DateTime",
	"DownloadCount":            "Edm.Int64",
	"VersionDownloadCount":     "Edm.Int64",
	"PackageSize":              "Edm.Int64",
}

var v2Properties = []string{
	"Id",
	"Version",
	"NormalizedVersion",
	"Title",
	"Description",
	"Summary",
	"ReleaseNotes",
	"Copyright",
	"Language",
	"Tags",
	"ProjectUrl",
	"IconUrl",
	"LicenseUrl",
	"RequireLicenseAcceptance",
	"Dependencies",
	"MinClientVersion",
	"IsPrerelease",
	"IsLatestVersion",
	"IsAbsoluteLatestVersion",
	"Listed",
	"Created",
	"LastUpdated",
	"Published",
	"DownloadCount",
	"VersionDownloadCount",
	"PackageHash",
	"PackageHashAlgorithm",
	"PackageSize",
	"Authors",
}

func writeV2Metadata(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("DataServiceVersion", "2.0")
	_, _ = w.Write(v2MetadataBody())
}

func v2MetadataBody() []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(
		`<edmx:Edmx xmlns:edmx="http://schemas.microsoft.com/ado/2007/06/edmx" Version="1.0"><edmx:DataServices xmlns:m="` + metadataNS + `" m:DataServiceVersion="2.0"><Schema xmlns="http://schemas.microsoft.com/ado/2008/09/edm" Namespace="NuGet"><EntityType Name="V2FeedPackage"><Key><PropertyRef Name="Id"/><PropertyRef Name="Version"/></Key>`,
	)
	for _, name := range v2Properties {
		typ := v2PropertyTypes[name]
		if typ == "" {
			typ = "Edm.String"
		}
		fmt.Fprintf(&b, `<Property Name="%s" Type="%s" Nullable="false"/>`, name, typ)
	}
	b.WriteString(
		`</EntityType><EntityContainer Name="NuGetContext" m:IsDefaultEntityContainer="true"><EntitySet Name="Packages" EntityType="NuGet.V2FeedPackage"/><FunctionImport Name="Search" ReturnType="Collection(NuGet.V2FeedPackage)" EntitySet="Packages" m:HttpMethod="GET"><Parameter Name="searchTerm" Type="Edm.String"/><Parameter Name="targetFramework" Type="Edm.String"/><Parameter Name="includePrerelease" Type="Edm.Boolean"/></FunctionImport><FunctionImport Name="FindPackagesById" ReturnType="Collection(NuGet.V2FeedPackage)" EntitySet="Packages" m:HttpMethod="GET"><Parameter Name="id" Type="Edm.String"/></FunctionImport></EntityContainer></Schema></edmx:DataServices></edmx:Edmx>`,
	)
	return b.Bytes()
}

// v2Entries calculates latest flags within the caller's visible version
// set before applying query filters. Package download totals also count
// only those versions, matching v3's non-disclosure rule.
func (h handler) v2Entries(r *http.Request, p pkg, keep v2Predicate) ([]atomEntry, error) {
	latest := ""
	for _, v := range p.versions {
		if !v.meta.IsPrerelease() {
			latest = v.pv.Version
		}
	}
	var selected []v2Version
	for _, v := range p.versions {
		vv := v2Version{
			meta:             v.meta,
			isLatest:         v.pv.Version == latest,
			isAbsoluteLatest: v.pv.Version == p.newest().pv.Version,
		}
		if keep(vv) {
			selected = append(selected, vv)
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	var total int64
	downloads := make(map[string]int64, len(p.versions))
	for _, v := range p.versions {
		n, err := h.downloads(r, v.meta)
		if err != nil {
			return nil, err
		}
		downloads[v.meta.Version] = n
		total += n
	}
	out := make([]atomEntry, 0, len(selected))
	for _, v := range selected {
		out = append(out, v2Atom(r, v, total, downloads[v.meta.Version]))
	}
	return out, nil
}

func v2Atom(r *http.Request, v v2Version, total, downloads int64) atomEntry {
	m := v.meta
	published := m.Published.UTC().Format(time.RFC3339Nano)
	var dependencies []string
	for _, group := range m.DependencyGroups {
		for _, dep := range group.Dependencies {
			dependencies = append(dependencies, dep.ID+":"+dep.Range+":"+group.TargetFramework)
		}
	}
	values := map[string]string{
		"Id":                m.ID,
		"Version":           m.Version,
		"NormalizedVersion": m.Version,
		"Title":             m.Title,
		"Description":       m.Description,
		"Summary":           m.Summary,
		"ReleaseNotes":      m.ReleaseNotes,
		"Copyright":         m.Copyright,
		"Language":          m.Language,
		"Tags":              m.Tags,
		"ProjectUrl":        m.ProjectURL,
		"IconUrl":           m.IconURL,
		"LicenseUrl":        m.LicenseURL,
		"RequireLicenseAcceptance": strconv.FormatBool(
			m.RequireLicenseAcceptance,
		),
		"Dependencies":     strings.Join(dependencies, "|"),
		"MinClientVersion": m.MinClientVersion,
		"IsPrerelease": strconv.FormatBool(
			m.IsPrerelease(),
		),
		"IsLatestVersion":         strconv.FormatBool(v.isLatest),
		"IsAbsoluteLatestVersion": strconv.FormatBool(v.isAbsoluteLatest),
		"Listed":                  "true",
		"Created":                 published,
		"LastUpdated":             published,
		"Published":               published,
		"DownloadCount":           strconv.FormatInt(total, 10),
		"VersionDownloadCount":    strconv.FormatInt(downloads, 10),
		"PackageHash":             m.SHA512,
		"PackageHashAlgorithm":    "SHA512",
		"PackageSize":             strconv.FormatInt(m.Size, 10),
		"Authors":                 m.Authors,
	}
	out := atomEntry{
		XMLNS:      atomNS,
		DataNS:     dataNS,
		MetadataNS: metadataNS,
		ID: v2Base(
			r,
		) + "/Packages(Id='" + url.PathEscape(
			m.ID,
		) + "',Version='" + url.PathEscape(
			m.Version,
		) + "')",
		Title:    m.ID,
		Summary:  m.Summary,
		Updated:  published,
		Category: atomCategory{Term: "NuGet.V2FeedPackage", Scheme: dataNS + "/scheme"},
		Content: atomContent{
			Type: "application/zip",
			Src:  v2Base(r) + "/Download/" + url.PathEscape(m.ID) + "/" + url.PathEscape(m.Version),
		},
	}
	for _, a := range splitAuthors(m.Authors) {
		out.Authors = append(out.Authors, atomAuthor{Name: a})
	}
	for _, name := range v2Properties {
		out.Properties.Items = append(
			out.Properties.Items,
			atomProperty{
				XMLName: xml.Name{Local: "d:" + name},
				Type:    v2PropertyTypes[name],
				Value:   values[name],
			},
		)
	}
	return out
}
