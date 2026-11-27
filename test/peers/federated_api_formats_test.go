package peers_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"

	apm "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentpackages"
	ml "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/machinelearning"
	nix "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	pub "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/peers"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccFederatedAPIFormatsSQLite(t *testing.T) {
	t.Parallel()
	apiFederationFormats(t, peers.SQLite)
}

func TestAccFederatedAPIFormatsPostgres(t *testing.T) {
	t.Parallel()
	apiFederationFormats(t, peers.Postgres)
}

// These six formats have no federated Terraform resource. Configure them
// through the repositories API, then publish complete native packages so
// each receiving process must reconstruct usable format metadata from items.
// HTTP protocol evidence here does not qualify native CLI compatibility.
func apiFederationFormats(t *testing.T, driver peers.Driver) {
	_, all := federation(t, driver)
	for _, tc := range []struct {
		name    string
		publish func(*testing.T, []*peers.Instance, string)
	}{
		{"pub", apiFederationPub},
		{"nix", apiFederationNix},
		{"machinelearning", apiFederationML},
		{"bazelmodules", apiFederationBazel},
		{"luarocks", apiFederationLuaRocks},
		{"agentpackages", apiFederationAPM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "fed-api-" + tc.name
			federate(t, all, key, tc.name)
			tc.publish(t, all, key)
		})
	}
}

func apiFederationRequest(t *testing.T, in *peers.Instance, method, path, body, media string, want int) (http.Header, string) {
	t.Helper()
	status, header, got := in.DoHeader(t, method, path, body, http.Header{"Content-Type": {media}})
	if status != want {
		t.Fatalf("%s %s on %s: %d %s, want %d", method, path, in.Name, status, got, want)
	}
	return header, got
}

// Only 404 may represent a publication that has not reached this member.
func apiFederationRead(t *testing.T, in *peers.Instance, path string) (string, bool) {
	t.Helper()
	status, body := in.Do(t, http.MethodGet, path, "")
	if status != http.StatusOK && status != http.StatusNotFound {
		t.Fatalf("GET %s on %s: %d %s, want 200 or 404", path, in.Name, status, body)
	}
	return body, status == http.StatusOK
}

// A generated link must resolve on the member answering the metadata,
// otherwise a receiver could appear healthy by sending clients to the writer.
func apiFederationLocalPath(t *testing.T, in *peers.Instance, link string) string {
	t.Helper()
	path, ok := strings.CutPrefix(link, in.URL+"/")
	if !ok {
		t.Fatalf("%s metadata linked outside this member: %q", in.Name, link)
	}
	return "/" + path
}

func apiFederationBytes(t *testing.T, in *peers.Instance, path, want string) {
	t.Helper()
	eventually(t, in, path+" with exact published bytes", func() (bool, string) {
		got, ok := apiFederationRead(t, in, path)
		return ok && got == want, fmt.Sprintf("found=%t bytes=%d sha256=%s", ok, len(got), apiFederationSHA(got))
	})
}

func apiFederationSHA(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func apiFederationForm(t *testing.T, field, filename, body string) (string, string) {
	t.Helper()
	var out bytes.Buffer
	w := multipart.NewWriter(&out)
	p, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.String(), w.FormDataContentType()
}

func apiFederationZIP(t *testing.T, entries [][2]string) string {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, entry := range entries {
		p, err := w.Create(entry[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.Write([]byte(entry[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func apiFederationPub(t *testing.T, all []*peers.Instance, key string) {
	archive := support.TarGz(t, map[string]string{
		"pubspec.yaml":           "name: federated_pub\nversion: 1.2.3\ndescription: controlled federation fixture\n",
		"lib/federated_pub.dart": "const message = 'federated';\n",
	})
	base := "/artifactory/api/pub/" + key
	body, media := apiFederationForm(t, "file", "package.tar.gz", archive)
	header, _ := apiFederationRequest(t, all[0], http.MethodPost, base+"/api/packages/versions/new", body, media, http.StatusNoContent)
	finish := apiFederationLocalPath(t, all[0], header.Get("Location"))
	_, answer := apiFederationRequest(t, all[0], http.MethodGet, finish, "", "", http.StatusOK)
	if !strings.Contains(answer, `"success"`) {
		t.Fatalf("Pub finalize: %s", answer)
	}
	awaitFile(t, all, key+"/"+pub.Path("federated_pub", "1.2.3"), archive)
	for _, in := range all {
		var link string
		eventually(t, in, "Pub version and archive digest", func() (bool, string) {
			body, ok := apiFederationRead(t, in, base+"/api/packages/federated_pub")
			var doc struct {
				Name     string `json:"name"`
				Versions []struct {
					Version    string `json:"version"`
					ArchiveURL string `json:"archive_url"`
					ArchiveSHA string `json:"archive_sha256"`
				} `json:"versions"`
			}
			if !ok || json.Unmarshal([]byte(body), &doc) != nil || doc.Name != "federated_pub" || len(doc.Versions) != 1 {
				return false, body
			}
			v := doc.Versions[0]
			link = v.ArchiveURL
			return v.Version == "1.2.3" && v.ArchiveSHA == apiFederationSHA(archive), body
		})
		apiFederationBytes(t, in, apiFederationLocalPath(t, in, link), archive)
	}
}

func apiFederationNix(t *testing.T, all []*peers.Instance, key string) {
	const hash = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc"
	const fixtures = "internal/artifacts/ecosystem/nix/testdata/cache/"
	meta := repopath.Read(t, fixtures+hash+".narinfo")
	info, err := nix.Parse(meta, hash+".narinfo")
	if err != nil {
		t.Fatal(err)
	}
	archive := string(repopath.Read(t, fixtures+info.URL))
	base := "/artifactory/api/nix/" + key + "/"
	apiFederationRequest(t, all[1], http.MethodPut, base+info.URL, archive, "application/octet-stream", http.StatusCreated)
	apiFederationRequest(t, all[1], http.MethodPut, base+hash+".narinfo", string(meta), "text/x-nix-narinfo", http.StatusCreated)
	awaitFile(t, all, key+"/"+nix.ArchiveItem(hash, info.URL, false), archive)
	awaitFile(t, all, key+"/"+nix.InfoItem(hash, false), string(meta))
	for _, in := range all {
		var link string
		eventually(t, in, "Nix store identity and projected archive URL", func() (bool, string) {
			body, ok := apiFederationRead(t, in, base+hash+".narinfo")
			link = "nar/" + hash + "/" + strings.TrimPrefix(info.URL, "nar/")
			return ok && body == string(info.Encode(link)), body
		})
		apiFederationBytes(t, in, base+link, archive)
	}
}

func apiFederationML(t *testing.T, all []*peers.Instance, key string) {
	base := "/artifactory/api/machinelearning/" + key + "/model/"
	_, body := apiFederationRequest(t, all[2], http.MethodPost, base+"classifier/start-transaction/1.2.3", "", "", http.StatusOK)
	var tx map[string]string
	if err := json.Unmarshal([]byte(body), &tx); err != nil || tx["transactionId"] == "" || tx["filesUploadPath"] == "" {
		t.Fatalf("ML transaction: %s (%v)", body, err)
	}
	const model = "controlled federated model bytes"
	apiFederationRequest(t, all[2], http.MethodPut, "/artifactory/"+key+"/"+tx["filesUploadPath"]+"/model.pkl", model, "application/octet-stream", http.StatusCreated)
	manifest := ml.Manifest{
		Created:   "2026-11-27T00:00:00Z",
		Artifacts: []ml.Artifact{{Path: "model.pkl", Size: int64(len(model)), Checksums: ml.Checksums{SHA256: apiFederationSHA(model)}}},
		Format:    ml.Serialization{Framework: "files", Format: "pkl", Runtime: "python", RuntimeVersion: "3.13"},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	form, media := apiFederationForm(t, "modelManifest", "modelManifest", string(data))
	apiFederationRequest(t, all[2], http.MethodPut, base+"entity-manifest/classifier/1.2.3/"+tx["transactionId"], form, media, http.StatusCreated)
	awaitFile(t, all, key+"/models/classifier/1.2.3/files/model.pkl", model)
	for _, in := range all {
		var link string
		eventually(t, in, "ML manifest of the committed version", func() (bool, string) {
			body, ok := apiFederationRead(t, in, base+"entity-manifest/classifier/1.2.3")
			var got ml.Manifest
			if !ok || json.Unmarshal([]byte(body), &got) != nil || len(got.Artifacts) != 1 {
				return false, body
			}
			link = got.Artifacts[0].DownloadURL
			return got.ID == "classifier" && got.Version == "1.2.3" && got.Created == manifest.Created &&
				got.Format == manifest.Format && got.Artifacts[0].Path == "model.pkl" &&
				got.Artifacts[0].Size == int64(len(model)) && got.Artifacts[0].Checksums.SHA256 == apiFederationSHA(model), body
		})
		apiFederationBytes(t, in, apiFederationLocalPath(t, in, link), model)
	}
}

func apiFederationBazel(t *testing.T, all []*peers.Instance, key string) {
	const module = "module(name = \"hello\", version = \"1.0.0\")\n"
	archive := support.TarGz(t, map[string]string{
		"hello-1.0.0/BUILD.bazel": "filegroup(name = \"all\", srcs = [\"message.txt\"], visibility = [\"//visibility:public\"])\n",
		"hello-1.0.0/message.txt": "hello from federation\n",
	})
	for _, f := range [][2]string{{"MODULE.bazel", module}, {"hello-1.0.0.tar.gz", archive}} {
		do(t, all[0], http.MethodPut, "/artifactory/"+key+"/modules/hello/1.0.0/"+f[0], f[1], http.StatusCreated)
	}
	for _, f := range [][2]string{{"MODULE.bazel", module}, {"hello-1.0.0.tar.gz", archive}} {
		awaitFile(t, all, key+"/modules/hello/1.0.0/"+f[0], f[1])
	}
	base := "/artifactory/api/bazelmodules/" + key
	sum := sha256.Sum256([]byte(archive))
	for _, in := range all {
		eventually(t, in, "Bazel version metadata", func() (bool, string) {
			body, ok := apiFederationRead(t, in, base+"/modules/hello/metadata.json")
			var got struct {
				Versions []string `json:"versions"`
			}
			return ok && json.Unmarshal([]byte(body), &got) == nil && slices.Equal(got.Versions, []string{"1.0.0"}), body
		})
		var link string
		eventually(t, in, "Bazel source integrity and archive location", func() (bool, string) {
			body, ok := apiFederationRead(t, in, base+"/modules/hello/1.0.0/source.json")
			var got struct {
				URL         string `json:"url"`
				Integrity   string `json:"integrity"`
				ArchiveType string `json:"archive_type"`
				StripPrefix string `json:"strip_prefix"`
			}
			if !ok || json.Unmarshal([]byte(body), &got) != nil {
				return false, body
			}
			link = got.URL
			return got.Integrity == "sha256-"+base64.StdEncoding.EncodeToString(sum[:]) && got.ArchiveType == "tar.gz" && got.StripPrefix == "hello-1.0.0", body
		})
		apiFederationBytes(t, in, base+"/modules/hello/1.0.0/MODULE.bazel", module)
		apiFederationBytes(t, in, apiFederationLocalPath(t, in, link), archive)
	}
}

func apiFederationLuaRocks(t *testing.T, all []*peers.Instance, key string) {
	const spec = "package = 'hello'\nversion = '1.0-1'\nsource = { url = 'file:///tmp/a' }\nbuild = { type = 'builtin', modules = {} }\n"
	rock := apiFederationZIP(t, [][2]string{{"hello-1.0-1.rockspec", spec}})
	base := "/artifactory/api/luarocks/" + key + "/"
	form, media := apiFederationForm(t, "rockspec_file", "hello-1.0-1.rockspec", spec)
	_, body := apiFederationRequest(t, all[1], http.MethodPost, base+"api/1/secret/upload", form, media, http.StatusOK)
	var result struct {
		Version struct {
			ID int64 `json:"id"`
		} `json:"version"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil || result.Version.ID == 0 {
		t.Fatalf("LuaRocks upload response: %s (%v)", body, err)
	}
	form, media = apiFederationForm(t, "rock_file", "hello-1.0-1.src.rock", rock)
	apiFederationRequest(t, all[1], http.MethodPost, fmt.Sprintf("%sapi/1/secret/upload_rock/%d", base, result.Version.ID), form, media, http.StatusOK)
	awaitFile(t, all, key+"/hello-1.0-1.rockspec", spec)
	awaitFile(t, all, key+"/hello-1.0-1.src.rock", rock)
	for _, in := range all {
		eventually(t, in, "LuaRocks manifest listing rockspec and source rock", func() (bool, string) {
			body, ok := apiFederationRead(t, in, base+"manifest")
			return ok && strings.Contains(body, `["hello"]`) && strings.Contains(body, `["1.0-1"]`) &&
				strings.Contains(body, `arch = "rockspec"`) && strings.Contains(body, `arch = "src"`), body
		})
		apiFederationBytes(t, in, base+"hello-1.0-1.rockspec", spec)
		apiFederationBytes(t, in, base+"hello-1.0-1.src.rock", rock)
	}
}

func apiFederationAPM(t *testing.T, all []*peers.Instance, key string) {
	archive := apiFederationZIP(t, [][2]string{
		{"apm.yml", "name: my-pkg\nversion: 1.2.3\ndescription: federation fixture\ntags: [assistant]\n"},
		{".apm/skills/help/SKILL.md", "---\nname: help\n---\n"},
	})
	base := "/artifactory/api/agentpackages/" + key + "/v1/packages/acme/my-pkg/versions"
	apiFederationRequest(t, all[2], http.MethodPut, base+"/1.2.3", archive, "application/zip", http.StatusCreated)
	awaitFile(t, all, key+"/"+apm.Path("acme", "my-pkg", "1.2.3"), archive)
	for _, in := range all {
		eventually(t, in, "APM package version and digest", func() (bool, string) {
			body, ok := apiFederationRead(t, in, base)
			var doc struct {
				Package  string `json:"package"`
				Versions []struct {
					Version string `json:"version"`
					Digest  string `json:"digest"`
				} `json:"versions"`
			}
			return ok && json.Unmarshal([]byte(body), &doc) == nil && doc.Package == "acme/my-pkg" &&
				len(doc.Versions) == 1 && doc.Versions[0].Version == "1.2.3" && doc.Versions[0].Digest == "sha256:"+apiFederationSHA(archive), body
		})
		apiFederationBytes(t, in, base+"/1.2.3/download", archive)
	}
}
