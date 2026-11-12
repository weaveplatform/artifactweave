package debian_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// The stable distribution these tests publish, and the files apt reads
// from it.
const (
	queryRelease     = "dists/stable/Release"
	queryInRelease   = "dists/stable/InRelease"
	queryPackages    = "dists/stable/main/binary-amd64/Packages"
	queryPackagesGz  = queryPackages + ".gz"
	querySources     = "dists/stable/main/source/Sources"
	queryTranslation = "dists/stable/main/i18n/Translation-en"
	queryPackage     = "pool/alpha_1.0_amd64.deb"
)

var (
	queryDeb   = []byte("alpha package bytes")
	queryIndex = fmt.Appendf(nil,
		"Package: alpha\nVersion: 1.0\nArchitecture: amd64\nFilename: %s\nSize: %d\nSHA256: %x\n\n",
		queryPackage, len(queryDeb), sha256.Sum256(queryDeb))
	querySourcesIndex = []byte("Package: alpha\nVersion: 1.0-1\nDirectory: pool/main/a/alpha\n" +
		"Files:\n " + strings.Repeat("b", 32) + " 1 alpha_1.0-1.dsc\n" +
		"Checksums-Sha256:\n " + strings.Repeat("c", 64) + " 1 alpha_1.0-1.dsc\n\n")
	queryByHash = fmt.Sprintf("dists/stable/main/binary-amd64/by-hash/SHA256/%x",
		sha256.Sum256(queryIndex))
)

// queryCaller is a caller of these tests: the user name the request
// authenticates as, or "" for an anonymous one.
type queryCaller struct{ name, as string }

var queryCallers = []queryCaller{
	{"admin", "admin"}, {"reader", "reader"}, {"user without read", "user"}, {"anonymous", ""},
}

// queryPut stores body as the file at path in the repository key, blob
// first, as a deploy leaves it, with props when there are any.
func (w *itemWorld) queryPut(key, path string, body []byte, props artmodel.Properties) {
	w.t.Helper()
	ctx := w.t.Context()
	upload, err := w.blobs.BeginUpload(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := upload.Append(ctx, bytes.NewReader(body)); err != nil {
		w.t.Fatal(err)
	}
	info, err := upload.Commit(ctx, blob.Digests{})
	if err != nil {
		w.t.Fatal(err)
	}
	it, err := w.db.Items().Put(ctx, artmodel.Item{
		RepoKey: key, Name: path, Size: info.Size, CreatedBy: "admin",
		Actual: kernel.Checksums{SHA256: info.Digests.SHA256},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	if len(props) != 0 {
		if err := w.db.Items().SetProperties(ctx, it.ID, props, time.Now().UTC()); err != nil {
			w.t.Fatal(err)
		}
	}
}

func queryGzip(t *testing.T, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// newQueryWorld is a server with anonymous access on and a local, a
// federated, a remote and a signing virtual Debian repository, the local
// and federated ones holding the stable distribution, whose plain Packages
// index carries the property origin=stored.
func newQueryWorld(t *testing.T) *itemWorld {
	t.Helper()
	w := newItemWorld(t, true)
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "deb-gpg", Type: api.KeypairGPG, PrivateKey: key.Private, PublicKey: key.Public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "deb-local", Class: repo.Local},
		{Key: "deb-fed", Class: repo.Federated},
		{Key: "deb-remote", Class: repo.Remote, URL: "https://deb.example"},
		{
			Key: "deb-virtual", Class: repo.Virtual, Repositories: []string{"deb-local"},
			KeyPair: "deb-gpg",
		},
	} {
		c.PackageType = repo.Debian
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	gz := queryGzip(t, queryIndex)
	release := fmt.Appendf(nil,
		"Suite: stable\nArchitectures: amd64\nComponents: main\nSHA256:\n %x %d %s\n %x %d %s\n",
		sha256.Sum256(queryIndex), len(queryIndex), "main/binary-amd64/Packages",
		sha256.Sum256(gz), len(gz), "main/binary-amd64/Packages.gz")
	for _, key := range []string{"deb-local", "deb-fed"} {
		w.queryPut(key, queryRelease, release, nil)
		w.queryPut(key, queryPackages, queryIndex, artmodel.Properties{"origin": {"stored"}})
		w.queryPut(key, queryPackagesGz, gz, nil)
		w.queryPut(key, queryByHash, queryIndex, nil)
		w.queryPut(key, querySources, querySourcesIndex, nil)
		w.queryPut(key, queryTranslation, []byte("translation bytes"), nil)
		w.queryPut(key, queryPackage, queryDeb, nil)
	}
	return w
}

// queryRefusal fails the test unless rec answered status with the
// Artifactory error envelope that carries message.
func queryRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}

// queryDenied fails the test unless rec refused caller as one without read
// permission is refused: 403, or 401 to an anonymous caller.
func queryDenied(t *testing.T, rec *httptest.ResponseRecorder, caller queryCaller) {
	t.Helper()
	if caller.as == "" {
		queryRefusal(t, rec, http.StatusUnauthorized, "Authentication is required")
		return
	}
	queryRefusal(t, rec, http.StatusForbidden, "Forbidden")
}

// dated matches the Date line a composed Release carries, which two
// compositions a second apart do not share.
var dated = regexp.MustCompile(`(?m)^Date: .*\n`)

// TestAccDebianStoredIndexWithQueryOrMatrixParameters (#1330): a local or
// federated repository's Packages index, gzipped form, by-hash copy and
// Sources index, asked for with a query or a matrix parameter, answer as
// without one: the index to an administrator and a reader, and the
// refusal of a caller without read permission, 403, or 401 to an
// anonymous caller (DV-057). Before, every one of them answered 404. A
// matrix parameter is matched against the index's own properties, as on
// any other file: a mandatory key it lacks, or a value it does not carry,
// is not found.
func TestAccDebianStoredIndexWithQueryOrMatrixParameters(t *testing.T) {
	w := newQueryWorld(t)
	gz := queryGzip(t, queryIndex)
	for _, key := range []string{"deb-local", "deb-fed"} {
		for _, tc := range []struct {
			target string
			body   []byte
		}{
			{queryPackages + "?skipUpdateStats=true", queryIndex},
			{queryPackages + "?unknown=1", queryIndex},
			{queryPackages + ";x=1", queryIndex},
			{queryPackages + ";origin=stored", queryIndex},
			{queryPackages + ";origin+=stored", queryIndex},
			{queryPackagesGz + "?skipUpdateStats=true", gz},
			{queryPackagesGz + ";x=1", gz},
			{queryByHash + "?skipUpdateStats=true", queryIndex},
			{querySources + "?skipUpdateStats=true", querySourcesIndex},
			{querySources + ";x=1", querySourcesIndex},
		} {
			for _, caller := range queryCallers {
				t.Run(key+"/"+tc.target+"/"+caller.name, func(t *testing.T) {
					rec := w.do(http.MethodGet, "/artifactory/"+key+"/"+tc.target, caller.as, "")
					if caller.as == "admin" || caller.as == "reader" {
						if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), tc.body) {
							t.Errorf("answered %d %q, want 200 and the index", rec.Code, rec.Body)
						}
						return
					}
					queryDenied(t, rec, caller)
				})
			}
		}
		for _, target := range []string{
			queryPackages + ";x+=1", queryPackages + ";origin=other",
			queryPackagesGz + ";origin+=stored",
		} {
			for _, caller := range queryCallers {
				t.Run(key+"/"+target+"/"+caller.name, func(t *testing.T) {
					rec := w.do(http.MethodGet, "/artifactory/"+key+"/"+target, caller.as, "")
					if caller.as == "admin" || caller.as == "reader" {
						queryRefusal(t, rec, http.StatusNotFound, "Could not find resource")
						return
					}
					queryDenied(t, rec, caller)
				})
			}
		}
		// The same path without a query, as it answered before.
		for _, caller := range queryCallers[2:] {
			t.Run(key+"/plain/"+caller.name, func(t *testing.T) {
				queryDenied(t, w.do(http.MethodGet, "/artifactory/"+key+"/"+queryPackages,
					caller.as, ""), caller)
			})
		}
	}
}

// TestAccDebianVirtualFilesWithQueryOrMatrixParameters (#1330): a virtual
// repository's composed Release, InRelease and Packages index, a dists/
// file it takes from a member, a member's Sources index and a package,
// asked for with a query or a matrix parameter, answer as without one: the
// file to an administrator and a reader, and the refusal of a caller
// without read permission, 403, or 401 to an anonymous caller. Before, the
// composed files and the package answered 403 to every caller, an
// administrator included, and the other dists/ files 404. A composed file
// carries no properties, so a mandatory matrix key refuses it as not
// found, as does a member's file that lacks one.
func TestAccDebianVirtualFilesWithQueryOrMatrixParameters(t *testing.T) {
	w := newQueryWorld(t)
	plain := func(path string) []byte {
		t.Helper()
		rec := w.do(http.MethodGet, "/artifactory/deb-virtual/"+path, "admin", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("plain %s answered %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.Bytes()
	}
	composed := map[string][]byte{}
	for _, path := range []string{queryRelease, queryPackages, queryPackagesGz} {
		composed[path] = plain(path)
	}
	for _, tc := range []struct {
		target, path string
		body         []byte
	}{
		{queryRelease + "?skipUpdateStats=true", queryRelease, nil},
		{queryRelease + ";x=1", queryRelease, nil},
		{queryInRelease + "?skipUpdateStats=true", queryInRelease, nil},
		{queryPackages + "?skipUpdateStats=true", queryPackages, nil},
		{queryPackagesGz + ";x=1", queryPackagesGz, nil},
		{queryTranslation + "?skipUpdateStats=true", "", []byte("translation bytes")},
		{queryTranslation + ";x=1", "", []byte("translation bytes")},
		{querySources + "?skipUpdateStats=true", "", querySourcesIndex},
		{querySources + ";x=1", "", querySourcesIndex},
		{queryPackage + "?skipUpdateStats=true", "", queryDeb},
		{queryPackage + ";x=1", "", queryDeb},
	} {
		for _, caller := range queryCallers {
			t.Run(tc.target+"/"+caller.name, func(t *testing.T) {
				rec := w.do(http.MethodGet, "/artifactory/deb-virtual/"+tc.target, caller.as, "")
				if caller.as != "admin" && caller.as != "reader" {
					queryDenied(t, rec, caller)
					return
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("answered %d %s, want 200", rec.Code, rec.Body)
				}
				switch {
				case tc.path == queryInRelease:
					if !bytes.Contains(
						rec.Body.Bytes(),
						[]byte("-----BEGIN PGP SIGNED MESSAGE-----"),
					) {
						t.Errorf("answered %q, want a signed Release", rec.Body)
					}
				case tc.path != "":
					got := dated.ReplaceAll(rec.Body.Bytes(), nil)
					if !bytes.Equal(got, dated.ReplaceAll(composed[tc.path], nil)) {
						t.Errorf("answered %q, want the composed %s", rec.Body, tc.path)
					}
				case !bytes.Equal(rec.Body.Bytes(), tc.body):
					t.Errorf("answered %q, want the member's file", rec.Body)
				}
			})
		}
	}
	for _, target := range []string{
		queryRelease + ";x+=1", queryPackages + ";x+=1", queryTranslation + ";x+=1",
		querySources + ";x+=1", queryPackage + ";x+=1",
	} {
		for _, caller := range queryCallers {
			t.Run(target+"/"+caller.name, func(t *testing.T) {
				rec := w.do(http.MethodGet, "/artifactory/deb-virtual/"+target, caller.as, "")
				if caller.as == "admin" || caller.as == "reader" {
					queryRefusal(t, rec, http.StatusNotFound, "Could not find resource")
					return
				}
				queryDenied(t, rec, caller)
			})
		}
	}
}

// TestAccDebianRemoteIndexRefusesAMatrixParameterOnceReadIsSettled
// (#1330, #1349): 7.x appends a request's matrix parameters to a remote
// repository's upstream URL (RTFACT-26349), which artifactweave's remote
// read does not do yet, so a remote Packages index keeps refusing one as
// not found, but only to a caller who may read it: one who may not is
// refused 403 first, as without a matrix parameter (DV-057). A query is
// served as without one (debian_remote_query_test.go).
func TestAccDebianRemoteIndexRefusesAMatrixParameterOnceReadIsSettled(t *testing.T) {
	w := newQueryWorld(t)
	target := queryPackages + ";x=1"
	queryRefusal(t, w.do(http.MethodGet, "/artifactory/deb-remote/"+target, "reader", ""),
		http.StatusNotFound, "Could not find resource")
	queryRefusal(t, w.do(http.MethodGet, "/artifactory/deb-remote/"+target, "user", ""),
		http.StatusForbidden, "Forbidden")
}
