package agentplugins_test

import (
	"crypto/md5"  //nolint:gosec // AQL's actual_md5 names an MD5
	"crypto/sha1" //nolint:gosec // AQL's actual_sha1 names a SHA-1
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// marketplaceNames are the files a local Agent Plugins repository keeps at
// its root.
var marketplaceNames = []string{
	"claude-marketplace.json", "codex-marketplace.json", "cursor-marketplace.json",
	"marketplace.json", "vscode-marketplace.json",
}

// cliQuery is the AQL that JFrog CLI 2.131.0 sends to find a marketplace
// file before it downloads it: jf agent plugins install without --version
// downloads <repo>/<file> with no SHA-256 or size, so jfrog-client-go
// searches for the file first (artifactory/services/download.go,
// 45c51cfca4f7) with the pattern's query (CreateAqlBodyForSpecWithPattern)
// and the download's fields, property included.
func cliQuery(key, name string) string {
	return `items.find({"$or":[{"$and":[{"repo":"` + key + `","path":".","name":"` + name +
		`"}]}]}).include("name","repo","path","actual_md5","actual_sha1","sha256","size",` +
		`"type","modified","created","property")`
}

// aqlHit is one AQL result with the fields the CLI's query includes.
type aqlHit struct {
	Repo       string `json:"repo"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	Created    string `json:"created"`
	Modified   string `json:"modified"`
	ActualSHA1 string `json:"actual_sha1"`
	ActualMD5  string `json:"actual_md5"`
	SHA256     string `json:"sha256"`
}

// TestAccAgentPluginMarketplaceFilesAreStoredFiles: 7.x keeps the
// marketplace files at the repository root (the Agent Plugins Repositories
// docs: Artifactory "rebuilds marketplace JSON at the repository root" and
// "maintains the following marketplace index files at the repository
// root"), and JFrog CLI's own tests find them with an AQL search before
// they download them. So the CLI's AQL search, the storage API's file
// info, the root folder listing and the repository's file list show each
// file, with the size, checksums and dates of the bytes its download
// serves, to every caller who may read the file's own path. A caller whose
// grant leaves out some plugins reads the same file, which lists them
// (issue 1292); one whose grant covers a single plugin's folder sees none
// of the files.
func TestAccAgentPluginMarketplaceFilesAreStoredFiles(t *testing.T) {
	w := newQueuedWorld(t)
	ctx := t.Context()
	const key = "plugins-local"
	c := repo.Config{Key: key, Class: repo.Local, PackageType: repo.AgentPlugins}
	if err := w.DB.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	for name, grant := range map[string]struct {
		user             string
		include, exclude []string
	}{
		"all-but-secret": {"deployer", []string{"**"}, []string{"secret/**"}},
		"alpha-only":     {"user", []string{"alpha/**"}, nil},
	} {
		if err := w.DB.Permissions().Create(ctx, accesstest.Permission{
			Name: name,
			Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]api.Actions{grant.user: api.ActionRead},
				},
				Targets: map[string]accesstest.Target{
					key: {Include: grant.include, Exclude: grant.exclude},
				},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"alpha", "secret"} {
		wantStatus(t, w.Do(http.MethodPut,
			"/artifactory/"+key+"/"+p+"/1.0.0/"+p+"-1.0.0.zip", "admin", pluginZIP(t, "cursor")),
			http.StatusCreated)
	}
	// The workers store the files once the uploads have answered.
	w.settle(key)

	for _, as := range []string{"admin", "deployer"} {
		for _, name := range marketplaceNames {
			t.Run(as+" "+name, func(t *testing.T) {
				hit := w.cliSearch(t, as, key, name)
				get := w.Do(http.MethodGet, "/artifactory/"+key+"/"+name, as, "")
				wantStatus(t, get, http.StatusOK)
				body := get.Body.Bytes()
				sha1Sum, md5Sum, sha256Sum := sha1.Sum(
					body,
				), md5.Sum(
					body,
				), sha256.Sum256(
					body,
				) //nolint:gosec // the fields' algorithms
				if hit.Repo != key || hit.Path != "." || hit.Name != name || hit.Type != "file" ||
					hit.Size != int64(len(body)) ||
					hit.ActualSHA1 != hex.EncodeToString(sha1Sum[:]) ||
					hit.ActualMD5 != hex.EncodeToString(md5Sum[:]) ||
					hit.SHA256 != hex.EncodeToString(sha256Sum[:]) ||
					hit.Created == "" || hit.Modified == "" {
					t.Errorf("AQL found %+v, want the file whose %d bytes the download serves",
						hit, len(body))
				}
				if got := get.Header().Get("X-Checksum-Sha1"); got != hit.ActualSHA1 {
					t.Errorf("download's X-Checksum-Sha1 %q, want AQL's %q", got, hit.ActualSHA1)
				}
				info := w.storage(t, as, key+"/"+name, http.StatusOK)
				checksums, _ := info["checksums"].(map[string]any)
				if info["path"] != "/"+name || info["size"] != strconv.Itoa(len(body)) ||
					info["createdBy"] != "_system_" || info["mimeType"] != "application/json" ||
					checksums["sha1"] != hit.ActualSHA1 || checksums["sha256"] != hit.SHA256 ||
					checksums["md5"] != hit.ActualMD5 {
					t.Errorf("storage API answered %v, want the file AQL found", info)
				}
				// The format sets no property on a marketplace file.
				wantStatus(t, w.Do(
					http.MethodGet,
					"/artifactory/api/storage/"+key+"/"+name+"?properties",
					as,
					"",
				), http.StatusNotFound)
			})
		}
		t.Run(as+" listings", func(t *testing.T) {
			root := w.storage(t, as, key+"/", http.StatusOK)
			var children []string
			for _, ch := range root["children"].([]any) {
				m := ch.(map[string]any)
				if m["folder"] == false {
					children = append(children, strings.TrimPrefix(m["uri"].(string), "/"))
				}
			}
			slices.Sort(children)
			if !slices.Equal(children, marketplaceNames) {
				t.Errorf("root lists the files %v, want %v", children, marketplaceNames)
			}
			list := w.storage(t, as, key+"?list&deep=0", http.StatusOK)
			var files []string
			for _, f := range list["files"].([]any) {
				m := f.(map[string]any)
				if m["folder"] == false && m["sha1"] != "" {
					files = append(files, strings.TrimPrefix(m["uri"].(string), "/"))
				}
			}
			slices.Sort(files)
			if !slices.Equal(files, marketplaceNames) {
				t.Errorf("the file list shows %v, want %v", files, marketplaceNames)
			}
		})
	}

	// deployer may not read secret's ZIP, and reads the stored file that
	// lists it, as every reader of the root does.
	wantStatus(t, w.Do(http.MethodGet, "/artifactory/"+key+"/secret/1.0.0/secret-1.0.0.zip",
		"deployer", ""), http.StatusForbidden)
	if body := w.Do(http.MethodGet, "/artifactory/"+key+"/cursor-marketplace.json",
		"deployer", "").Body.String(); !strings.Contains(body, `"secret/1.0.0/secret-1.0.0.zip"`) {
		t.Errorf("deployer's Cursor file %s, want the stored file, which lists secret", body)
	}

	// user may read alpha's folder alone: no AQL search, storage call or
	// listing shows a marketplace file, and the download refuses it.
	for _, name := range marketplaceNames {
		if hits := w.aql(t, "user", cliQuery(key, name)); len(hits) != 0 {
			t.Errorf("AQL showed user %+v", hits)
		}
		w.storage(t, "user", key+"/"+name, http.StatusForbidden)
		wantStatus(t, w.Do(http.MethodGet, "/artifactory/"+key+"/"+name, "user", ""),
			http.StatusForbidden)
	}
	root := w.storage(t, "user", key+"/", http.StatusOK)
	if got, _ := json.Marshal(root["children"]); string(got) != `[{"folder":true,"uri":"/alpha"}]` {
		t.Errorf("user's root lists %s, want alpha alone", got)
	}
}

// cliSearch runs cliQuery as the user as and returns its one result.
func (w *queuedWorld) cliSearch(t *testing.T, as, key, name string) aqlHit {
	t.Helper()
	hits := w.aql(t, as, cliQuery(key, name))
	if len(hits) != 1 {
		t.Fatalf("AQL found %+v for %s, want the file alone", hits, name)
	}
	return hits[0]
}

// aql runs query as the user as and returns its results.
func (w *queuedWorld) aql(t *testing.T, as, query string) []aqlHit {
	t.Helper()
	rec := w.Do(http.MethodPost, "/artifactory/api/search/aql", as, query,
		"Content-Type", "text/plain")
	wantStatus(t, rec, http.StatusOK)
	var doc struct {
		Results []aqlHit `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("AQL answered %s: %v", rec.Body, err)
	}
	return doc.Results
}

// storage GETs /artifactory/api/storage/<path> as the user as, wants the
// status, and returns the JSON body of a 200.
func (w *queuedWorld) storage(t *testing.T, as, path string, status int) map[string]any {
	t.Helper()
	rec := w.Do(http.MethodGet, "/artifactory/api/storage/"+path, as, "")
	wantStatus(t, rec, status)
	if status != http.StatusOK {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("storage API answered %s: %v", rec.Body, err)
	}
	return doc
}
