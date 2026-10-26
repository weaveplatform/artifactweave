package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cucumber/godog"
)

const gitLFSAsset = "Native Git LFS client bytes, unchanged after the round trip.\n"

func (w *world) registerGitLFS(sc *godog.ScenarioContext) {
	sc.Step(`^I publish and freshly clone a Git LFS asset through "([^"]*)"$`, w.gitLFSPublishClone)
	sc.Step(
		`^I publish and freshly clone a Git LFS asset through "([^"]*)" as "([^"]*)" with password "([^"]*)"$`,
		w.gitLFSPublishCloneAs,
	)
	sc.Step(
		`^I attempt a Git LFS publish through "([^"]*)" as "([^"]*)" with password "([^"]*)"$`,
		w.gitLFSPublishDenied,
	)
	sc.Step(`^a native Git LFS upstream serves an unchanged object$`, w.gitLFSStartUpstream)
	sc.Step(`^I exercise two-user Git LFS locking through "([^"]*)"$`, w.gitLFSLockTwoUsers)
	sc.Step(
		`^I list Git LFS locks through "([^"]*)" with the pinned client$`,
		w.gitLFSListRemoteLocks,
	)
	sc.Step(
		`^I list Git LFS locks through "([^"]*)" with the pinned client in plain mode$`,
		w.gitLFSListRemoteLocksPlain,
	)
	sc.Step(`^the native Git LFS upstream stops$`, w.gitLFSStopUpstream)
	sc.Step(
		`^I fetch the upstream Git LFS asset through "([^"]*)" in a fresh client "([^"]*)"$`,
		w.gitLFSFetch,
	)
}

func (w *world) gitLFSListRemoteLocks(key string) error {
	return w.gitLFSListRemoteLocksWithMode(key, "json")
}

func (w *world) gitLFSListRemoteLocksPlain(key string) error {
	return w.gitLFSListRemoteLocksWithMode(key, "plain")
}

func (w *world) gitLFSListRemoteLocksWithMode(key, mode string) error {
	return w.runContainer(
		w.gitLFSImage,
		[]string{
			"-e", "LFS_URL=" + w.gitLFSClientURL() + "/artifactory/api/lfs/" + key,
			"-e", "LFS_MODE=" + mode,
		},
		[]string{"sh", "-eu", "-c", `
git lfs version
git config --global credential.helper '!f() { echo username=admin; echo password=acceptance-admin; }; f'
worktree=$(mktemp -d)
git init -q "$worktree"
cd "$worktree"
git lfs install --local >/dev/null
git config user.email client@example.invalid
git config user.name 'Git LFS client'
printf '[lfs]\n\turl = %s\n' "$LFS_URL" > .lfsconfig
printf '*.bin filter=lfs diff=lfs merge=lfs -text lockable\n' > .gitattributes
printf 'A worktree file protected by Git LFS locking.\n' > asset.bin
git add .lfsconfig .gitattributes asset.bin
git commit -qm initial
git remote add origin https://example.invalid/unused.git
if [ "$LFS_MODE" = plain ]; then
  git lfs locks
else
  git lfs locks --json
fi
`},
	)
}

func (w *world) gitLFSLockTwoUsers(key string) error {
	return w.runContainer(
		w.gitLFSImage,
		[]string{"-e", "LFS_URL=" + w.gitLFSClientURL() + "/artifactory/api/lfs/" + key},
		[]string{"sh", "-eu", "-c", `
git lfs version
git config --global credential.helper '!f() { echo username=$LFS_USER; echo password=$LFS_PASS; }; f'
for who in alice bob; do
  git init -q "$who"
  cd "$who"
  git config user.email "$who@example.invalid"
  git config user.name "$who"
  git lfs install --local >/dev/null
  printf '[lfs]\n\turl = %s\n' "$LFS_URL" > .lfsconfig
  printf '*.bin filter=lfs diff=lfs merge=lfs -text lockable\n' > .gitattributes
  printf 'A worktree file protected by Git LFS locking.\n' > asset.bin
  git add .lfsconfig .gitattributes asset.bin
  git commit -qm initial
  git remote add origin https://example.invalid/unused.git
  cd ..
done
LFS_USER=lfs-alice LFS_PASS=LFS-Alice-123 git -C alice lfs lock asset.bin
LFS_USER=lfs-alice LFS_PASS=LFS-Alice-123 git -C alice lfs locks | grep -F asset.bin
LFS_USER=lfs-bob LFS_PASS=LFS-Bob-123 git -C bob lfs locks --verify --json > verify.json
grep -E '"theirs"[[:space:]]*:' verify.json
grep -F lfs-alice verify.json
if LFS_USER=lfs-bob LFS_PASS=LFS-Bob-123 git -C bob lfs lock asset.bin; then
  echo 'duplicate lock unexpectedly succeeded' >&2; exit 1
fi
if LFS_USER=lfs-bob LFS_PASS=LFS-Bob-123 git -C bob lfs unlock asset.bin; then
  echo 'other user unlock unexpectedly succeeded' >&2; exit 1
fi
LFS_USER=lfs-bob LFS_PASS=LFS-Bob-123 git -C bob lfs unlock --force asset.bin
LFS_USER=lfs-bob LFS_PASS=LFS-Bob-123 git -C bob lfs locks --verify --json > after.json
grep -E '"theirs"[[:space:]]*:[[:space:]]*\[\]' after.json
echo 'two-user Git LFS locking passed'
`},
	)
}

func (w *world) gitLFSPublishClone(key string) error {
	return w.gitLFSPublishCloneAs(key, "admin", "acceptance-admin")
}

func (w *world) gitLFSPublishCloneAs(key, user, password string) error {
	return w.runContainer(
		w.gitLFSImage,
		[]string{
			"-e", "LFS_URL=" + w.gitLFSClientURL() + "/artifactory/api/lfs/" + key,
			"-e", "LFS_USER=" + user, "-e", "LFS_PASSWORD=" + password,
		},
		[]string{"sh", "-eu", "-c", `
git lfs version
git lfs install >/dev/null
git config --global credential.helper '!f() { echo "username=$LFS_USER"; echo "password=$LFS_PASSWORD"; }; f'
git init -q source
cd source
git config user.email client@example.invalid
git config user.name 'Git LFS client'
	git config credential.helper '!f() { echo "username=$LFS_USER"; echo "password=$LFS_PASSWORD"; }; f'
git lfs install --local >/dev/null
printf '[lfs]\n\turl = %s\n' "$LFS_URL" > .lfsconfig
printf '*.bin filter=lfs diff=lfs merge=lfs -text\n' > .gitattributes
printf 'Native Git LFS client bytes, unchanged after the round trip.\n' > asset.bin
expected=$(sha256sum asset.bin | cut -d' ' -f1)
git add .lfsconfig .gitattributes asset.bin
git commit -qm publish
git remote add origin https://example.invalid/unused.git
git lfs push origin HEAD
rm -rf .git/lfs/objects
cd ..
git clone -q source clone
test "$(sha256sum clone/asset.bin | cut -d' ' -f1)" = "$expected"
git -C clone lfs env | grep -F "$LFS_URL"
`},
	)
}

func (w *world) gitLFSPublishDenied(key, user, password string) error {
	return w.runContainer(w.gitLFSImage, []string{
		"-e", "LFS_URL=" + w.gitLFSClientURL() + "/artifactory/api/lfs/" + key,
		"-e", "LFS_USER=" + user, "-e", "LFS_PASSWORD=" + password,
	}, []string{"sh", "-eu", "-c", `
git lfs version
git init -q denied
cd denied
git config user.email client@example.invalid
git config user.name 'Git LFS denied client'
git config credential.helper '!f() { echo "username=$LFS_USER"; echo "password=$LFS_PASSWORD"; }; f'
git lfs install --local >/dev/null
printf '[lfs]\n\turl = %s\n' "$LFS_URL" > .lfsconfig
printf '*.bin filter=lfs diff=lfs merge=lfs -text\n' > .gitattributes
printf 'Access denied to this Git LFS deploy target.\n' > asset.bin
git add .lfsconfig .gitattributes asset.bin
git commit -qm denied
git remote add origin https://example.invalid/unused.git
git lfs push origin HEAD
`})
}

func (w *world) gitLFSStartUpstream() error {
	sum := sha256.Sum256([]byte(gitLFSAsset))
	w.gitLFSOID = hex.EncodeToString(sum[:])
	var origin *httptest.Server
	origin = httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		prefix := "/artifactory/api/lfs/origin/objects/"
		switch r.URL.Path {
		case "/artifactory/api/lfs/origin/locks":
			if r.Method != http.MethodGet ||
				r.Header.Get("Accept") != "application/vnd.git-lfs+json" ||
				r.Header.Get("Content-Type") != "application/vnd.git-lfs+json" {
				http.Error(out, "invalid lock list", http.StatusBadRequest)
				return
			}
			out.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(out).Encode(map[string]any{"locks": []any{
				map[string]any{
					"id":        "upstream-lock-1",
					"path":      "asset.bin",
					"locked_at": "2026-10-26T18:00:00Z",
					"owner":     map[string]string{"name": "upstream-user"},
				},
			}})
		case prefix + "batch":
			if r.Method != http.MethodPost ||
				r.Header.Get("Content-Type") != "application/vnd.git-lfs+json" {
				http.Error(out, "invalid batch", http.StatusBadRequest)
				return
			}
			var request struct {
				Operation string `json:"operation"`
				Objects   []struct {
					OID  string `json:"oid"`
					Size int64  `json:"size"`
				} `json:"objects"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&request) != nil ||
				request.Operation != "download" || len(request.Objects) != 1 ||
				request.Objects[0].OID != w.gitLFSOID || request.Objects[0].Size != int64(len(gitLFSAsset)) {
				http.Error(out, "invalid object", http.StatusUnprocessableEntity)
				return
			}
			out.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(out).
				Encode(map[string]any{"transfer": "basic", "objects": []any{
					map[string]any{
						"oid":  w.gitLFSOID,
						"size": len(gitLFSAsset),
						"actions": map[string]any{
							"download": map[string]any{"href": origin.URL + prefix + w.gitLFSOID},
						},
					},
				}})
		case prefix + w.gitLFSOID:
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				out.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			out.Header().Set("Content-Length", strconv.Itoa(len(gitLFSAsset)))
			out.Header().Set("Content-Type", "application/octet-stream")
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(out, gitLFSAsset)
			}
		default:
			http.NotFound(out, r)
		}
	}))
	w.gitLFSUpstream = origin
	w.closers = append(w.closers, origin.Close)
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["upstream"] = origin.URL + "/artifactory/api/lfs/origin"
	return nil
}

func (w *world) gitLFSStopUpstream() {
	if w.gitLFSUpstream != nil {
		w.gitLFSUpstream.Close()
	}
}

func (w *world) gitLFSFetch(key, profile string) error {
	if w.gitLFSOID == "" || profile != "cold" && profile != "warm" {
		return fmt.Errorf("invalid Git LFS fetch fixture")
	}
	return w.runContainer(w.gitLFSImage, []string{
		"-e", "LFS_URL=" + w.gitLFSClientURL() + "/artifactory/api/lfs/" + key,
		"-e", "LFS_OID=" + w.gitLFSOID,
		"-e", "LFS_SIZE=" + strconv.Itoa(len(gitLFSAsset)),
		"-e", "LFS_PROFILE=" + profile,
	}, []string{"sh", "-eu", "-c", `
git lfs version
mkdir "$LFS_PROFILE"
cd "$LFS_PROFILE"
git init -q
git config credential.helper '!f() { echo username=admin; echo password=acceptance-admin; }; f'
git lfs install --local >/dev/null
printf '[lfs]\n\turl = %s\n' "$LFS_URL" > .lfsconfig
printf 'version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %s\n' "$LFS_OID" "$LFS_SIZE" | git lfs smudge > asset.bin
test "$(sha256sum asset.bin | cut -d' ' -f1)" = "$LFS_OID"
`})
}

func (w *world) gitLFSClientURL() string {
	if host := os.Getenv("ARTIFACTWEAVE_ACCEPT_DOCKER_HOST"); host != "" {
		return strings.Replace(w.url, "127.0.0.1", host, 1)
	}
	if runtime.GOOS == "darwin" && os.Getenv("ARTIFACTWEAVE_GITLFS_DESKTOP") == "1" {
		return strings.Replace(w.url, "127.0.0.1", "host.docker.internal", 1)
	}
	return w.url
}
