//go:build e2e

package acceptance

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

func (p *upstreamProbe) protocol() {
	switch p.family {
	case "generic":
		p.artifact("/artifactory/"+p.repo()+"/README.md", p.pin.SHA256)
	case "maven", "ivy", "gradle", "sbt":
		root := "/artifactory/" + p.repo() + "/org/slf4j/slf4j-api/"
		metadata, _ := p.request(
			"version-metadata",
			http.MethodGet,
			root+"maven-metadata.xml",
			nil,
			"",
			200,
			"",
		)
		if !strings.Contains(string(metadata), "<version>2.0.17</version>") {
			p.fail("version-metadata", "protocol_assertion", "pinned Maven version absent")
		}
		p.artifact(root+"2.0.17/slf4j-api-2.0.17.jar", p.pin.SHA256)
	case "npm":
		p.npmProtocol()
	case "pypi":
		p.pypiProtocol()
	case "nuget-v3":
		p.nugetV3Protocol()
	case "nuget-v2":
		root := "/artifactory/api/nuget/" + p.repo() + "/"
		data, _ := p.request(
			"v2-feed",
			http.MethodGet,
			root+"FindPackagesById()?id=%27Newtonsoft.Json%27&semVerLevel=2.0.0",
			nil,
			"application/atom+xml",
			200,
			"",
		)
		if !strings.Contains(string(data), "Newtonsoft.Json") {
			p.fail("v2-feed", "protocol_assertion", "pinned package absent from Atom feed")
		}
		p.artifact(root+"Download/Newtonsoft.Json/13.0.3", p.pin.SHA256)
	case "helm":
		p.helmProtocol()
	case "docker", "oci", "helmoci":
		p.ociProtocol()
	case "terraform-module":
		p.moduleProtocol()
	case "terraform-provider":
		p.providerProtocol()
	}
}

func (p *upstreamProbe) artifact(target, digest string) {
	p.request("artifact", http.MethodGet, target, nil, "", 200, digest)
}

func (p *upstreamProbe) npmProtocol() {
	root := "/artifactory/api/npm/" + p.repo() + "/is-number"
	for _, accept := range []string{"application/json", "application/vnd.npm.install-v1+json"} {
		data, _ := p.request("packument", http.MethodGet, root, nil, accept, 200, "")
		var doc struct {
			Name     string                     `json:"name"`
			Versions map[string]json.RawMessage `json:"versions"`
		}
		p.decode(data, &doc)
		if doc.Name != "is-number" || doc.Versions["7.0.0"] == nil {
			p.fail("packument", "protocol_assertion", "package/version identity changed")
		}
	}
	data, _ := p.request(
		"version-metadata",
		http.MethodGet,
		root+"/7.0.0",
		nil,
		"application/json",
		200,
		"",
	)
	var doc struct {
		Version string `json:"version"`
		Dist    struct {
			Tarball   string `json:"tarball"`
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	p.decode(data, &doc)
	if doc.Version != "7.0.0" || doc.Dist.Integrity == "" {
		p.fail("version-metadata", "protocol_assertion", "version or upstream integrity absent")
	}
	p.artifact(doc.Dist.Tarball, p.pin.SHA256)
}

func (p *upstreamProbe) pypiProtocol() {
	root := "/artifactory/api/pypi/" + p.repo() + "/simple/six/"
	data, _ := p.request("simple-html", http.MethodGet, root, nil, "text/html", 200, "")
	if !strings.Contains(string(data), "six-1.17.0-py2.py3-none-any.whl") {
		p.fail("simple-html", "protocol_assertion", "pinned wheel absent")
	}
	data, _ = p.request(
		"simple-json",
		http.MethodGet,
		root,
		nil,
		"application/vnd.pypi.simple.v1+json",
		200,
		"",
	)
	var doc struct {
		Files []struct {
			Filename     string          `json:"filename"`
			URL          string          `json:"url"`
			CoreMetadata json.RawMessage `json:"core-metadata"`
		} `json:"files"`
	}
	p.decode(data, &doc)
	var pins []upstreamPin
	p.decode(p.pins["pypi"], &pins)
	for _, pin := range pins {
		found := false
		for _, file := range doc.Files {
			if file.Filename != pin.Filename {
				continue
			}
			found = true
			p.artifact(file.URL, pin.SHA256)
			if strings.HasSuffix(file.Filename, ".whl") && len(file.CoreMetadata) > 0 &&
				string(file.CoreMetadata) != "false" {
				var hashes map[string]string
				if json.Unmarshal(file.CoreMetadata, &hashes) == nil && hashes["sha256"] != "" {
					u, err := url.Parse(file.URL)
					if err != nil {
						p.fail("core-metadata", "protocol_assertion", "invalid wheel link")
					}
					u.Path += ".metadata"
					u.Fragment = ""
					p.request(
						"core-metadata",
						http.MethodGet,
						u.String(),
						nil,
						"",
						200,
						hashes["sha256"],
					)
				}
			}
		}
		if !found {
			p.fail("simple-json", "protocol_assertion", "pinned distribution absent")
		}
	}
}

func (p *upstreamProbe) nugetV3Protocol() {
	root := "/artifactory/api/nuget/v3/" + p.repo() + "/"
	data, _ := p.request(
		"service-index",
		http.MethodGet,
		root+"index.json",
		nil,
		"application/json",
		200,
		"",
	)
	var doc struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}
	p.decode(data, &doc)
	base := ""
	for _, r := range doc.Resources {
		if r.Type == "PackageBaseAddress/3.0.0" {
			base = r.ID
		}
	}
	if base == "" {
		p.fail("service-index", "protocol_assertion", "flat-container resource absent")
	}
	data, _ = p.request(
		"version-metadata",
		http.MethodGet,
		base+"newtonsoft.json/index.json",
		nil,
		"application/json",
		200,
		"",
	)
	if !strings.Contains(string(data), `"13.0.3"`) {
		p.fail("version-metadata", "protocol_assertion", "pinned NuGet version absent")
	}
	p.artifact(base+"newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg", p.pin.SHA256)
}

func (p *upstreamProbe) helmProtocol() {
	data, _ := p.request(
		"chart-index",
		http.MethodGet,
		"/artifactory/api/helm/"+p.repo()+"/index.yaml",
		nil,
		"",
		200,
		"",
	)
	var doc struct {
		Entries map[string][]struct {
			Version string   `yaml:"version"`
			URLs    []string `yaml:"urls"`
			Digest  string   `yaml:"digest"`
		} `yaml:"entries"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		p.fail("chart-index", "protocol_assertion", "invalid YAML index")
	}
	for _, entry := range doc.Entries["podinfo"] {
		if entry.Version != "6.7.1" {
			continue
		}
		if len(entry.URLs) == 0 || entry.Digest != p.pin.SHA256 {
			p.fail("chart-index", "protocol_assertion", "chart digest/URL differs from pin")
		}
		// Relative chart links resolve against the actual downstream index URL.
		base, _ := url.Parse(p.w.url + "/artifactory/api/helm/" + p.repo() + "/")
		link, err := url.Parse(entry.URLs[0])
		if err != nil {
			p.fail("chart-index", "protocol_assertion", "invalid chart URL")
		}
		p.artifact(base.ResolveReference(link).String(), p.pin.SHA256)
		return
	}
	p.fail("chart-index", "protocol_assertion", "pinned chart absent")
}

func (p *upstreamProbe) ociProtocol() {
	root := "/v2/" + p.repo() + "/" + p.pin.Repository + "/"
	data, header := p.request(
		"manifest",
		http.MethodGet,
		root+"manifests/"+p.pin.Digest,
		nil,
		"application/vnd.oci.image.manifest.v1+json",
		200,
		strings.TrimPrefix(p.pin.Digest, "sha256:"),
	)
	if header.Get("Docker-Content-Digest") != p.pin.Digest {
		p.fail("manifest", "protocol_assertion", "registry digest header differs from manifest pin")
	}
	type descriptor struct {
		Digest string `json:"digest"`
		Size   int    `json:"size"`
	}
	var doc struct {
		Config descriptor   `json:"config"`
		Layers []descriptor `json:"layers"`
	}
	p.decode(data, &doc)
	if len(doc.Layers) > 4 {
		p.fail("manifest", "protocol_assertion", "fixture layer count exceeds four")
	}
	for _, entry := range append([]descriptor{doc.Config}, doc.Layers...) {
		if !strings.HasPrefix(entry.Digest, "sha256:") || entry.Size > 8<<20 {
			p.fail("blob", "protocol_assertion", "fixture blob exceeds its digest/size budget")
		}
		p.artifact(root+"blobs/"+entry.Digest, strings.TrimPrefix(entry.Digest, "sha256:"))
	}
}

func (p *upstreamProbe) moduleProtocol() {
	root := "/artifactory/api/terraform/v1/modules/" + p.repo() + "__cloudposse/label/null/"
	data, _ := p.request(
		"module-versions",
		http.MethodGet,
		root+"versions",
		nil,
		"application/json",
		200,
		"",
	)
	if !strings.Contains(string(data), `"0.25.0"`) {
		p.fail("module-versions", "protocol_assertion", "pinned module version absent")
	}
	_, header := p.request(
		"module-location",
		http.MethodGet,
		root+"0.25.0/download",
		nil,
		"",
		204,
		"",
	)
	location := header.Get("X-Terraform-Get")
	u, err := url.Parse(location)
	if err != nil || location == "" {
		p.fail("module-location", "protocol_assertion", "download location absent or malformed")
	}
	if index := strings.Index(u.Path, "//"); index >= 0 {
		u.Path = u.Path[:index]
	}
	p.artifact(u.String(), p.pin.SHA256)
}

func (p *upstreamProbe) providerProtocol() {
	root := "/artifactory/api/terraform/" + p.repo() + "/v1/providers/hashicorp/null/"
	data, _ := p.request(
		"provider-versions",
		http.MethodGet,
		root+"versions",
		nil,
		"application/json",
		200,
		"",
	)
	if !strings.Contains(string(data), `"3.3.2"`) {
		p.fail("provider-versions", "protocol_assertion", "pinned provider version absent")
	}
	data, _ = p.request(
		"provider-download",
		http.MethodGet,
		root+"3.3.2/download/linux/amd64",
		nil,
		"application/json",
		200,
		"",
	)
	var doc struct {
		DownloadURL  string `json:"download_url"`
		SumsURL      string `json:"shasums_url"`
		SignatureURL string `json:"shasums_signature_url"`
		Shasum       string `json:"shasum"`
		Keys         struct {
			GPG []struct {
				ID string `json:"key_id"`
			} `json:"gpg_public_keys"`
		} `json:"signing_keys"`
	}
	p.decode(data, &doc)
	if doc.Shasum != p.pin.SHA256 || len(doc.Keys.GPG) == 0 ||
		doc.Keys.GPG[0].ID != "34365D9472D7468F" {
		p.fail(
			"provider-download",
			"protocol_assertion",
			"original SHA-256 or advertised signer changed",
		)
	}
	p.artifact(doc.DownloadURL, p.pin.SHA256)
	p.request("provider-checksums", http.MethodGet, doc.SumsURL, nil, "", 200, "")
	p.request("provider-signature", http.MethodGet, doc.SignatureURL, nil, "", 200, "")
}
