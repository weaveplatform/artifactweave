package mavenhttp

import (
	"net/http"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/fault"
)

// The jar the generatePom tests name, and where its POM goes.
const (
	libJar = "com/acme/lib/1.0/lib-1.0.jar"
	libPom = "com/acme/lib/1.0/lib-1.0.pom"
)

// TestAccMavenCalculateMetadataQueuesTheFolder: calculateMetadata answers
// 200 with an empty plain-text body once it has queued the metadata of the
// packages under the folder, or only of those whose files lie directly in
// it when noneRecursive is true. The repository root names the whole
// repository.
func TestAccMavenCalculateMetadataQueuesTheFolder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		queued bool
	}{
		{"folder", calc + "maven-local/com/acme", true},
		{"folder, noneRecursive false", calc + "maven-local/com/acme?noneRecursive=false", true},
		{"folder alone", calc + "maven-local/com/acme?noneRecursive=true", false},
		{"version folder alone", calc + "maven-local/com/acme/app/1.0?noneRecursive=true", true},
		{"repository root", calc + "maven-local/", true},
		{"repository root alone", calc + "maven-local/?noneRecursive=true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t)
			w.folder("com/acme")
			w.folder("com/acme/app/1.0")
			w.file("com/acme/app/1.0/app-1.0.pom", []byte(pom("com.acme", "app", "1.0")))
			rec := w.do(http.MethodPost, tc.target, admin)
			if rec.Code != http.StatusOK || rec.Body.Len() != 0 ||
				rec.Header().Get("Content-Type") != "text/plain" {
				t.Fatalf("answered %d %q %s, want 200 with an empty text/plain body",
					rec.Code, rec.Header().Get("Content-Type"), rec.Body)
			}
			if got := w.queued(maven.KindMetadata, "maven-local/com.acme:app"); got != tc.queued {
				t.Errorf("com.acme:app's metadata queued %t, want %t", got, tc.queued)
			}
		})
	}
}

// TestAccMavenCalculateMetadataNeedsEveryContentAction: calculateMetadata
// needs Manage, Delete/Overwrite, Deploy/Cache, Annotate and Read on the
// folder, as JFrog's REST reference lists them; Manage alone, or all of
// them but one, is refused, and the anonymous user is challenged.
func TestAccMavenCalculateMetadataNeedsEveryContentAction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		as      *caller
		status  int
		message string
	}{
		{"every content action", holding(calculateMetadataActions), http.StatusOK, ""},
		{"Manage alone", holding(accessapi.ActionManage), http.StatusForbidden, "Forbidden"},
		{
			"every content action but Annotate",
			holding(calculateMetadataActions &^ accessapi.ActionAnnotate),
			http.StatusForbidden, "Forbidden",
		},
		{"anonymous user", anonymous, http.StatusUnauthorized, "Authentication is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t)
			w.folder("com/acme")
			rec := w.do(http.MethodPost, calc+"maven-local/com/acme", tc.as)
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
				return
			}
			if rec.Code != http.StatusOK {
				t.Errorf("answered %d %s, want 200", rec.Code, rec.Body)
			}
		})
	}
}

// TestAccMavenOperationsAnswerArtifactorysRefusals: both operations resolve
// their target as Artifactory does: a malformed path or noneRecursive
// answers 400, a missing repository or path 404, a repository that is not
// local 404, one outside the Maven group 400, and a caller who may not act
// on the path 403 before anything about the repository is told. generatePom
// also needs a file the layout reads Maven coordinates from.
func TestAccMavenOperationsAnswerArtifactorysRefusals(t *testing.T) {
	w := newItemWorld(t)
	w.folder("com/acme")
	w.file("com/acme/readme.txt", []byte("readme"))
	w.file(libJar, []byte("jar"))
	for _, tc := range []struct {
		name    string
		target  string
		as      *caller
		status  int
		message string
	}{
		{
			"noneRecursive that is no boolean", calc + "maven-local/com/acme?noneRecursive=maybe",
			admin, http.StatusBadRequest, "noneRecursive must be true or false",
		},
		{
			"path with a dot-dot segment", calc + "maven-local/com/../acme", admin,
			http.StatusBadRequest, `malformed path "com/../acme"`,
		},
		{
			"no repository key", calc + "/", admin,
			http.StatusBadRequest, "a repository key is required",
		},
		{
			"absent repository", calc + "absent/com", admin,
			http.StatusNotFound, "Could not find path 'absent/com'",
		},
		{
			"local repository's cache", calc + "maven-local-cache/com", admin,
			http.StatusNotFound, "Could not find path 'maven-local-cache/com'",
		},
		{
			"absent folder", calc + "maven-local/com/none", admin,
			http.StatusNotFound, "Could not find path 'maven-local/com/none'",
		},
		{
			"remote repository", calc + "maven-remote/com", admin,
			http.StatusNotFound, "Unable to find local repository 'maven-remote'.",
		},
		{
			"remote repository's cache", calc + "maven-remote-cache/com", admin,
			http.StatusNotFound, "Unable to find local repository 'maven-remote-cache'.",
		},
		{
			"repository outside the Maven group", calc + "libs-local/com", admin,
			http.StatusBadRequest, "Repository 'libs-local' is not a maven repository.",
		},
		{
			"caller who may not act on a remote repository", calc + "maven-remote/com", user,
			http.StatusForbidden, "Forbidden",
		},
		{
			"generatePom of the root", gen + "maven-local/", admin,
			http.StatusBadRequest, "'maven-local/' is not a file",
		},
		{
			"generatePom of a folder", gen + "maven-local/com/acme", admin,
			http.StatusBadRequest, "'maven-local/com/acme' is not a file",
		},
		{
			"generatePom of a file without coordinates", gen + "maven-local/com/acme/readme.txt",
			admin, http.StatusBadRequest, "Cannot generate a POM for 'com/acme/readme.txt': " +
				"the repository layout reads no Maven coordinates from it",
		},
		{
			"generatePom of an absent jar", gen + "maven-local/com/acme/lib/1.0/lib-2.0.jar", admin,
			http.StatusNotFound, "Could not find path 'maven-local/com/acme/lib/1.0/lib-2.0.jar'",
		},
		{
			"generatePom in a remote repository", gen + "maven-remote/" + libJar, admin,
			http.StatusNotFound, "Unable to find local repository 'maven-remote'.",
		},
		{
			"generatePom in a repository outside the Maven group", gen + "libs-local/" + libJar,
			admin, http.StatusBadRequest, "Repository 'libs-local' is not a maven repository.",
		},
		{
			"generatePom for a caller who may not read the jar", gen + "maven-local/" + libJar,
			user, http.StatusForbidden, "Forbidden",
		},
		{
			"generatePom for the anonymous user", gen + "maven-local/" + libJar, anonymous,
			http.StatusUnauthorized, "Authentication is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, w.do(http.MethodPost, tc.target, tc.as), tc.status, tc.message)
		})
	}
	if _, ok := w.content(libPom); ok {
		t.Error("a refused generatePom wrote a POM")
	}
}

// TestAccMavenOperationsServeTheMavenGroup: the four package types of the
// Maven group share the operations, so a Gradle repository is served as a
// Maven one.
func TestAccMavenOperationsServeTheMavenGroup(t *testing.T) {
	w := newItemWorld(t)
	rec := w.do(http.MethodPost, calc+gradleLocal+"/", admin)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("answered %d %s, want 200 with an empty body", rec.Code, rec.Body)
	}
}

// TestAccMavenGeneratePomWritesTheJarsPOM: generatePom writes the POM a jar
// carries for its own coordinates, or a default one, through the deploy
// rules, and answers 200 with no body; a POM already there is kept, and a
// POM the deploy rules refuse is answered with their status and message.
func TestAccMavenGeneratePomWritesTheJarsPOM(t *testing.T) {
	own := pom("com.acme", "lib", "1.0")
	for _, tc := range []struct {
		name     string
		jar      []byte
		existing string
		status   int
		message  string
		// want is what the stored POM holds afterwards, each string a part
		// of it; nil wants no POM.
		want []string
	}{
		{
			name: "jar without a POM", jar: []byte("jar"), status: http.StatusOK,
			want: []string{
				"<groupId>com.acme</groupId>", "<artifactId>lib</artifactId>",
				"<version>1.0</version>", "<packaging>jar</packaging>",
				"<description>Artifactory auto generated POM</description>",
			},
		},
		{
			name: "jar with its own POM", jar: jar(t, own),
			status: http.StatusOK, want: []string{own},
		},
		{
			name: "POM already there", jar: jar(t, own), existing: "hand-written",
			status: http.StatusOK, want: []string{"hand-written"},
		},
		{
			name: "jar whose POM names another version", jar: jar(t, pom("com.acme", "lib", "3.0")),
			status: http.StatusConflict,
			message: "The target deployment path '" + libPom + "' does not match the POM's " +
				"expected path prefix 'com/acme/lib/3.0'. Please verify your POM content for " +
				"correctness and make sure the source path is a valid Maven repository root path.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t)
			w.file(libJar, tc.jar)
			if tc.existing != "" {
				w.file(libPom, []byte(tc.existing))
			}
			rec := w.do(http.MethodPost, gen+"maven-local/"+libJar, admin)
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
			} else if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
				t.Errorf("answered %d %s, want 200 with no body", rec.Code, rec.Body)
			}
			got, ok := w.content(libPom)
			if ok != (tc.want != nil) {
				t.Fatalf("POM stored %t, want %t: %s", ok, tc.want != nil, got)
			}
			for _, part := range tc.want {
				if !strings.Contains(got, part) {
					t.Errorf("POM lacks %s:\n%s", part, got)
				}
			}
		})
	}
}

// TestAccMavenGeneratePomNeedsReadOnTheJarAndDeployOnThePOM: generatePom
// reads the jar, whose content the POM may come from, and writes the POM,
// so the caller needs Read on the jar and Deploy where the POM goes, as
// writing it by hand would take.
func TestAccMavenGeneratePomNeedsReadOnTheJarAndDeployOnThePOM(t *testing.T) {
	for _, tc := range []struct {
		name   string
		holds  accessapi.Actions
		status int
	}{
		{"Read and Deploy", accessapi.ActionRead | accessapi.ActionWrite, http.StatusOK},
		{"Read alone", accessapi.ActionRead, http.StatusForbidden},
		{"Deploy alone", accessapi.ActionWrite, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t)
			w.file(libJar, []byte("jar"))
			rec := w.do(http.MethodPost, gen+"maven-local/"+libJar, holding(tc.holds))
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, "Forbidden")
			} else if rec.Code != http.StatusOK {
				t.Errorf("answered %d %s, want 200", rec.Code, rec.Body)
			}
			if _, ok := w.content(libPom); ok != (tc.status == http.StatusOK) {
				t.Errorf("POM written %t, want %t", ok, tc.status == http.StatusOK)
			}
		})
	}
}

// TestAccMavenOperationsFailClosed: a server without a store answers both
// operations as unimplemented, a request that carries no caller is
// challenged, and a caller whose permissions cannot be evaluated, a
// repository or path that cannot be looked up, files that cannot be listed
// and a POM that cannot be stored answer 500, naming nothing internal and
// logging the step that failed.
func TestAccMavenOperationsFailClosed(t *testing.T) {
	folder := calc + "maven-local/com/acme"
	generate := gen + "maven-local/" + libJar
	for _, tc := range []struct {
		name    string
		tune    func(*itemkit.Deps)
		target  string
		as      *caller
		status  int
		message string
		logged  string
	}{
		{
			name:   "calculateMetadata without a store",
			tune:   func(d *itemkit.Deps) { d.Store = nil },
			target: folder, as: admin,
			status: http.StatusNotImplemented, message: "not implemented by artifactweave yet",
		},
		{
			name:   "generatePom without a store",
			tune:   func(d *itemkit.Deps) { d.Store = nil },
			target: generate, as: admin,
			status: http.StatusNotImplemented, message: "not implemented by artifactweave yet",
		},
		{
			name:   "calculateMetadata with no caller",
			target: folder,
			status: http.StatusUnauthorized, message: "Authentication is required",
		},
		{
			name:   "generatePom with no caller",
			target: generate,
			status: http.StatusUnauthorized, message: "Authentication is required",
		},
		{
			name:   "permissions cannot be evaluated",
			tune:   func(d *itemkit.Deps) { d.Access.Authorizer = failingAuthorizer },
			target: folder, as: &caller{p: kernel.Principal{Name: "user"}},
			status: http.StatusInternalServerError, message: "internal error",
			logged: "item permissions",
		},
		{
			name:   "repository lookup fails",
			tune:   func(d *itemkit.Deps) { d.Store = repoLookupFails{d.Store} },
			target: folder, as: admin,
			status: http.StatusInternalServerError, message: "internal error",
			logged: "resolve repository",
		},
		{
			name:   "path lookup fails",
			tune:   func(d *itemkit.Deps) { d.Store = itemLookupFails{d.Store} },
			target: generate, as: admin,
			status: http.StatusInternalServerError, message: "internal error",
			logged: "get item",
		},
		{
			name:   "files cannot be listed",
			tune:   func(d *itemkit.Deps) { d.Store = fileSearchFails{d.Store} },
			target: folder, as: admin,
			status: http.StatusInternalServerError, message: "internal error",
			logged: "calculate Maven metadata",
		},
		{
			name: "POM cannot be stored",
			tune: func(d *itemkit.Deps) {
				d.Blobs = fault.Blobs{Store: d.Blobs, Begin: errFault}
			},
			target: generate, as: admin,
			status: http.StatusInternalServerError, message: "internal error",
			logged: "deploy POM",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tune []func(*itemkit.Deps)
			if tc.tune != nil {
				tune = append(tune, tc.tune)
			}
			w := newItemWorld(t, tune...)
			w.folder("com/acme")
			w.file(libJar, []byte("jar"))
			wantError(t, w.do(http.MethodPost, tc.target, tc.as), tc.status, tc.message)
			if tc.logged != "" {
				w.wantLogged(t, tc.logged)
			}
			if _, ok := w.content(libPom); ok {
				t.Error("a failed request wrote a POM")
			}
		})
	}
}
