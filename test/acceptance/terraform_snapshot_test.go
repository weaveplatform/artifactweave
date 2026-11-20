package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type terraformSnapshotCase struct {
	name, source, ref, directory string
}

const terraformSnapshotRegistryURL = "https://registry.snapshot.test"

// This native-only control isolates a real local Git repository and permits only
// file transport. It documents why accepting a commit with positive depth for an
// HTTP snapshot does not claim equivalent shallow-clone behavior.
func TestAccTerraformGitCloneDepthControl(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git is required for the isolated native clone control")
	}
	for _, client := range []struct{ name, env, version string }{
		{"Terraform", "ARTIFACTWEAVE_TERRAFORM_BIN", "1.17.1"},
		{"OpenTofu", "ARTIFACTWEAVE_TOFU_BIN", "1.13.9"},
	} {
		t.Run(client.name, func(t *testing.T) {
			program := os.Getenv(client.env)
			if program == "" {
				if os.Getenv("CI") == "true" {
					t.Fatal("required pinned native clone-control client is absent")
				}
				t.Skip("pinned native client is not configured")
			}
			w := newWorld(t)
			root := t.TempDir()
			config := filepath.Join(root, "empty.rc")
			if err := os.WriteFile(config, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			env := []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root,
				"TF_CLI_CONFIG_FILE=" + config, "TOFU_CLI_CONFIG_FILE=" + config,
				"NETRC=" + config, "TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1",
				"GIT_CONFIG_GLOBAL=" + config, "GIT_CONFIG_SYSTEM=" + config, "GIT_CONFIG_NOSYSTEM=1",
				"GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file",
				"GIT_AUTHOR_NAME=artifactweave fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
				"GIT_COMMITTER_NAME=artifactweave fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
			}
			if _, err := w.terraformGetterCommand(git, root, env, "init", "--initial-branch=main", "repository"); err != nil {
				t.Fatal(err)
			}
			repository := filepath.Join(root, "repository")
			if err := os.WriteFile(filepath.Join(repository, "main.tf"), []byte("output \"identity\" { value = \"clone-control\" }\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "main.tf"}, {"commit", "-m", "isolated native module fixture"}, {"tag", "v1"}} {
				if _, err := w.terraformGetterCommand(git, repository, env, args...); err != nil {
					t.Fatal(err)
				}
			}
			out, err := w.terraformGetterCommand(git, repository, env, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			commit := strings.TrimSpace(string(out))
			address := (&url.URL{Scheme: "file", Path: repository}).String()
			for _, control := range []struct {
				name, query string
				refused     bool
			}{
				{"tag", "ref=v1", false},
				{"branch_depth", "ref=main&depth=1", false},
				{"commit", "ref=" + commit, false},
				{"commit_depth", "ref=" + commit + "&depth=1", true},
			} {
				dir := t.TempDir()
				childEnv := append(append([]string(nil), env...), "HOME="+dir, "TMPDIR="+dir)
				body := fmt.Sprintf("module \"control\" { source = %q }\noutput \"identity\" { value = module.control.identity }\n", "git::"+address+"?"+control.query)
				if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				info, err := w.terraformGetterCommand(program, dir, childEnv, "version", "-json")
				var pin struct {
					Version string `json:"terraform_version"`
				}
				if err != nil || json.Unmarshal(info, &pin) != nil || pin.Version != client.version {
					t.Fatal("native clone-control executable pin mismatch")
				}
				out, err := w.terraformGetterCommand(program, dir, childEnv, "init", "-backend=false", "-input=false", "-no-color")
				if control.refused {
					if err == nil || !bytes.Contains(out, []byte("not found")) || !bytes.Contains(out, []byte(commit)) {
						t.Fatal("commit with positive clone depth did not fail as an unavailable named ref")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.terraformGetterCommand(program, dir, childEnv, "apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
						t.Fatal(err)
					}
					out, err := w.terraformGetterCommand(program, dir, childEnv, "output", "-raw", "identity")
					if err != nil || string(out) != "clone-control" {
						t.Fatal("native clone-control module identity changed")
					}
				}
				t.Logf("NATIVE_CLONE_CONTROL client=%s version=%s form=%s expectedRefusal=%t PASS", client.name, client.version, control.name, control.refused)
			}
		})
	}
}

// The fixture replaces only DNS, certificate trust and the physical dial address.
// The actual upstream pool still checks destinations, TLS names, redirects and
// credential origins; the actual runtime/router/store serves every client request.
func TestAccTerraformGitHubSnapshots(t *testing.T) {
	for _, client := range []struct{ name, env, version string }{
		{"Terraform", "ARTIFACTWEAVE_TERRAFORM_BIN", "1.17.1"},
		{"OpenTofu", "ARTIFACTWEAVE_TOFU_BIN", "1.13.9"},
	} {
		for _, backend := range []string{"sqlite", "postgres"} {
			for _, selection := range []string{"remote", "virtual"} {
				t.Run(client.name+"/"+backend+"/"+selection, func(t *testing.T) {
					program := os.Getenv(client.env)
					if program == "" {
						if os.Getenv("CI") == "true" {
							t.Fatal("required pinned native snapshot client is absent")
						}
						t.Skip("pinned native client is not configured")
					}
					if !filepath.IsAbs(program) {
						t.Fatal("native snapshot client must be an absolute executable path")
					}
					w, github, registry := newTerraformSnapshotWorld(t, backend, selection)
					cases := terraformSnapshotCases()
					originals := seedTerraformSnapshots(t, github, registry, cases)
					readRepo := "tf-snapshots"
					if selection == "virtual" {
						readRepo += "-all"
					}
					w.runTerraformSnapshots(t, program, client.version, backend, readRepo, github, registry, cases, originals)
				})
			}
		}
	}
}

func terraformSnapshotCases() []terraformSnapshotCase {
	commit := strings.Repeat("a", 40)
	return []terraformSnapshotCase{
		{"head", "github.com/acme/modules", "HEAD", "root"},
		{"dot_git", "github.com/acme/modules.git", "HEAD", "root"},
		{"path", "github.com/acme/modules/modules/demo?ref=v1.0", "v1.0", "root/modules/demo"},
		{"selector", "github.com/acme/modules//modules/demo?ref=v1.0", "v1.0", "root/modules/demo"},
		{"combined", "github.com/acme/modules/modules//demo?ref=v1.0&depth=1", "v1.0", "root/modules/demo"},
		{"forced", "git::https://github.com/acme/modules.git//modules/demo?ref=v1.0", "v1.0", "root/modules/demo"},
		{"branch", "github.com/acme/modules/modules/demo?ref=feature%2Fnative", "feature/native", "root/modules/demo"},
		{"qualified_branch", "github.com/acme/modules/modules/demo?ref=refs%2Fheads%2Fsame", "refs/heads/same", "root/modules/demo"},
		{"qualified_tag", "github.com/acme/modules/modules/demo?ref=refs%2Ftags%2Fsame", "refs/tags/same", "root/modules/demo"},
		{"unqualified_collision", "github.com/acme/modules/modules/demo?ref=same", "same", "root/modules/demo"},
		{"commit", "github.com/acme/modules/modules/demo?ref=" + commit, commit, "root/modules/demo"},
		{"commit_depth", "github.com/acme/modules/modules/demo?ref=" + commit + "&depth=1", commit, "root/modules/demo"},
		{"lfs_pointer", "github.com/acme/modules/modules/demo?ref=lfs-pointer", "lfs-pointer", "root/modules/demo"},
		{"lfs_materialized", "github.com/acme/modules/modules/demo?ref=lfs-materialized", "lfs-materialized", "root/modules/demo"},
	}
}

func newTerraformSnapshotWorld(t *testing.T, backend, selection string) (*world, *fakeupstream.Server, *fakeupstream.Server) {
	t.Helper()
	certificate, roots := terraformSnapshotCertificate(t)
	github := fakeupstream.New(t, fakeupstream.Options{TLS: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}})
	registry := fakeupstream.New(t, fakeupstream.Options{TLS: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}})
	rt := harness.App(t, func(c *config.Config) {
		c.Bootstrap.AdminPassword = testAdminPassword
		c.Database.Driver = backend
		if backend == "postgres" {
			c.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		}
	})
	sources := rt.Artifacts.RemoteSources()
	policy := sources.HTTP.(*maven.MavenBackend).Policy
	resolve, err := upstream.NewResolver(rt.Store, rt.Keys, upstream.ResolverOptions{
		RevisionKey: []byte(strings.Repeat("s", 32)), RootCAsPEM: roots,
		AllowedPrivateOrigins: []string{"https://api.github.com", "https://github.com", "https://codeload.github.com", terraformSnapshotRegistryURL},
		LookupIP: func(_ context.Context, host string) ([]net.IPAddr, error) {
			switch host {
			case "api.github.com", "github.com", "codeload.github.com", "127.0.0.1":
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			case "registry.snapshot.test":
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.2")}}, nil
			default:
				return nil, remote.ErrSecurity
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	originalOptions := policy.Options
	physical, _ := url.Parse(github.URL)
	registryPhysical, _ := url.Parse(registry.URL)
	policy.Options = func(ctx context.Context, c repo.Config) (remote.Options, error) {
		current, err := originalOptions(ctx, c)
		if err != nil {
			return current, err
		}
		opts, err := resolve.Options(ctx, c)
		if err != nil {
			return opts, err
		}
		// Fixture trust/DNS/dial bindings are immutable for this runtime. Retain
		// production's credential revision and its current-authority callback.
		opts.Revision = current.Revision
		wrap := opts.WrapTransport
		opts.WrapTransport = func(tr *http.Transport) (http.RoundTripper, func()) {
			dial := tr.DialContext
			tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address == "127.0.0.1:443" {
					address = physical.Host
				} else if address == "127.0.0.2:443" {
					address = registryPhysical.Host
				}
				return dial(ctx, network, address)
			}
			return wrap(tr)
		}
		return opts, nil
	}
	w := newWorld(t)
	h := harness.Router(harness.Config{
		Store: rt.Store, Blobs: rt.Blobs, Auth: rt.Auth, Tokens: rt.Tokens,
		Keys: support.TerraformBackendKeys(t), RemoteSources: sources,
		RemoteHTTP: rt.Artifacts.RemoteHTTP(), VirtualCache: rt.Artifacts.VirtualCache(),
		AllowBasicAuthTokens: true, Logger: slog.New(slog.NewJSONHandler(&w.stderr, nil)),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	w.url = srv.URL
	for _, c := range []repo.Config{
		{Key: "tf-snapshots", Class: repo.Remote, PackageType: repo.Terraform, URL: "https://api.github.com", TerraformRegistryURL: terraformSnapshotRegistryURL},
		{Key: "tf-snapshots-all", Class: repo.Virtual, PackageType: repo.Terraform, Repositories: []string{"tf-snapshots"}},
	} {
		if err := rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w, github, registry
}

func terraformSnapshotCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "controlled GitHub snapshot fixture"},
		DNSNames:  []string{"api.github.com", "github.com", "codeload.github.com", "registry.snapshot.test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func seedTerraformSnapshots(t *testing.T, github, registry *fakeupstream.Server, cases []terraformSnapshotCase) map[string][]byte {
	t.Helper()
	putJSON := func(path string, doc any) {
		body, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		registry.Set(path, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/json"}}})
	}
	putJSON("/.well-known/terraform.json", map[string]string{"modules.v1": "/registry/modules/"})
	var versions []map[string]string
	originals := map[string][]byte{}
	for i, c := range cases {
		version := fmt.Sprintf("%d.0.1", i+1)
		versions = append(versions, map[string]string{"version": version})
		registry.Set("/registry/modules/acme/snapshots/aws/"+version+"/download", fakeupstream.Resource{Status: 204, Header: http.Header{"X-Terraform-Get": {c.source}}})
		if originals[c.ref] == nil {
			body := terraformSnapshotArchive(t, c.ref)
			originals[c.ref] = body
			// The actual guarded upstream client follows the GitHub-to-codeload
			// redirect, preserving logical TLS names and dropping origin secrets.
			github.Set("/acme/modules/archive/"+c.ref+".tar.gz", fakeupstream.Resource{Redirect: "https://codeload.github.com/acme/modules/tar.gz/" + url.PathEscape(c.ref)})
			github.Set("/acme/modules/tar.gz/"+c.ref, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/gzip"}}})
		}
	}
	versions = append(versions, map[string]string{"version": "99.0.1"})
	registry.Set(
		"/registry/modules/acme/snapshots/aws/99.0.1/download",
		fakeupstream.Resource{Status: 204, Header: http.Header{"X-Terraform-Get": {"github.com/acme/modules/modules/demo?ref=absent-submodule"}}},
	)
	github.Set(
		"/acme/modules/archive/absent-submodule.tar.gz",
		fakeupstream.Resource{Body: terraformSnapshotArchive(t, "absent-submodule"), Header: http.Header{"Content-Type": {"application/gzip"}}},
	)
	putJSON("/registry/modules/acme/snapshots/aws/versions", map[string]any{"modules": []any{map[string]any{"versions": versions}}})
	return originals
}

func terraformSnapshotArchive(t *testing.T, identity string) []byte {
	t.Helper()
	config := fmt.Sprintf("output \"identity\" { value = %q }\n", identity)
	if identity == "lfs-pointer" || identity == "lfs-materialized" {
		config = "output \"identity\" { value = file(\"${path.module}/asset.bin\") }\n"
	}
	demo := "module \"sibling\" { source = \"../shared\" }\noutput \"identity\" { value = module.sibling.identity }\n"
	if identity == "absent-submodule" {
		demo = "module \"vendor\" { source = \"../vendor\" }\noutput \"identity\" { value = module.vendor.identity }\n"
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, file := range []struct{ name, body string }{
		{"root/main.tf", config},
		{"root/modules/demo/main.tf", demo},
		{"root/modules/shared/main.tf", config},
		{"root/asset.bin", terraformSnapshotValue(identity)},
		{"root/modules/shared/asset.bin", terraformSnapshotValue(identity)},
		{"root/.lfsconfig", "[lfs]\n url = https://codeload.github.com/lfs/objects\n"},
		{"root/.gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n"},
		{"root/.gitmodules", "[submodule \"vendor\"]\n path = modules/vendor\n url = https://github.com/vendor/missing.git\n"},
		{"root/README.md", "complete immutable source snapshot\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: 0o600, Size: int64(len(file.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func terraformSnapshotValue(ref string) string {
	switch ref {
	case "lfs-pointer":
		return "version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("b", 64) + "\nsize 24\n"
	case "lfs-materialized":
		return "materialized artifact bytes\n"
	default:
		return ref
	}
}

func (w *world) runTerraformSnapshots(
	t *testing.T,
	program, version, backend, readRepo string,
	github, registry *fakeupstream.Server,
	cases []terraformSnapshotCase,
	originals map[string][]byte,
) {
	t.Helper()
	if err := w.send("POST", "/access/api/v1/tokens", `{"expires_in":3600}`, getterAdmin); err != nil {
		t.Fatal(err)
	}
	if err := w.statusIs(200); err != nil {
		t.Fatal(err)
	}
	var token struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(w.body, &token); err != nil || token.Access == "" {
		t.Fatal("snapshot native fixture did not obtain its Access token")
	}
	host, services, discovery := strings.TrimPrefix(w.url, "http://"), "", "HTTPS"
	if runtime.GOOS == "darwin" {
		var err error
		services, err = w.terraformRegistryServices()
		if err != nil {
			t.Fatal(err)
		}
		discovery = "local-service-override"
	} else {
		if err := w.frontWithTLSBase(true); err != nil {
			t.Fatal(err)
		}
		host = w.vars["tlshost"]
	}
	for _, phase := range []string{"cold", "warm-moved-ref", "offline-fresh-client"} {
		if phase == "warm-moved-ref" {
			github.Set(
				"/acme/modules/tar.gz/feature/native",
				fakeupstream.Resource{Body: terraformSnapshotArchive(t, "moved-branch"), Header: http.Header{"Content-Type": {"application/gzip"}}},
			)
		}
		if phase == "offline-fresh-client" {
			github.Close()
			registry.Close()
			body, _ := json.Marshal(
				map[string]any{
					"rclass":               "remote",
					"packageType":          "terraform",
					"url":                  "https://api.github.com",
					"terraformRegistryUrl": terraformSnapshotRegistryURL,
					"offline":              true,
				},
			)
			if err := w.send("POST", "/artifactory/api/repositories/tf-snapshots", string(body), getterAdmin); err != nil {
				t.Fatal(err)
			}
			if err := w.statusIs(200); err != nil {
				t.Fatal(err)
			}
		}
		before := len(github.Requests()) + len(registry.Requests())
		dir := t.TempDir()
		cliConfig := filepath.Join(dir, "client.rc")
		if err := os.WriteFile(cliConfig, fmt.Appendf([]byte(services), "credentials %q { token = %q }\n", host, token.Access), 0o600); err != nil {
			t.Fatal(err)
		}
		var root strings.Builder
		for i, c := range cases {
			fmt.Fprintf(&root, "module %q {\n source = %q\n version = %q\n}\noutput %q { value = module.%s.identity }\n", c.name,
				host+"/"+readRepo+"__acme/snapshots/aws", fmt.Sprintf("%d.0.1", i+1), c.name, c.name)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(root.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		netrc := filepath.Join(dir, "empty.netrc")
		if err := os.WriteFile(netrc, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		env := []string{
			"HOME=" + dir, "TMPDIR=" + dir, "PATH=" + os.Getenv("PATH"), "TF_CLI_CONFIG_FILE=" + cliConfig,
			"TOFU_CLI_CONFIG_FILE=" + cliConfig, "NETRC=" + netrc, "SSL_CERT_FILE=" + w.tlsCA,
			"TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1",
		}
		info, err := w.terraformGetterCommand(program, dir, env, "version", "-json")
		var pin struct {
			Version string `json:"terraform_version"`
		}
		if err != nil || json.Unmarshal(info, &pin) != nil || pin.Version != version {
			t.Fatal("snapshot native client pin mismatch")
		}
		w.markCommand()
		if _, err := w.terraformGetterCommand(program, dir, env, "init", "-backend=false", "-input=false", "-no-color"); err != nil {
			t.Fatal(err)
		}
		if err := w.answeredNo500(); err != nil {
			t.Fatal(err)
		}
		if _, err := w.terraformGetterCommand(program, dir, env, "apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
			t.Fatal(err)
		}
		output, err := w.terraformGetterCommand(program, dir, env, "output", "-json")
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]struct{ Value string }
		if err := json.Unmarshal(output, &values); err != nil {
			t.Fatal(err)
		}
		var manifest struct {
			Modules []struct{ Key, Version, Dir string }
		}
		data, err := os.ReadFile(filepath.Join(dir, ".terraform/modules/modules.json"))
		if err != nil || json.Unmarshal(data, &manifest) != nil {
			t.Fatal("snapshot native manifest is missing or malformed")
		}
		for i, c := range cases {
			if values[c.name].Value != terraformSnapshotValue(c.ref) {
				t.Fatalf("snapshot %s lost selected ref identity", c.name)
			}
			selected, sibling := false, c.directory == "root"
			for _, m := range manifest.Modules {
				selected = selected || (m.Key == c.name && m.Version == fmt.Sprintf("%d.0.1", i+1) && strings.HasSuffix(m.Dir, c.directory))
				sibling = sibling || (m.Key == c.name+".sibling" && strings.HasSuffix(m.Dir, "root/modules/shared"))
			}
			if !selected || !sibling {
				t.Fatalf("snapshot %s lost version, selected directory or sibling", c.name)
			}
			path := "/artifactory/tf-snapshots/acme/modules/tags/" + c.ref + "/modules-" + c.ref + ".tar.gz"
			if err := w.send("GET", path, "", getterAdmin); err != nil {
				t.Fatal(err)
			}
			if w.status != 200 || !bytes.Equal(w.body, originals[c.ref]) {
				t.Fatalf("snapshot %s original archive identity changed", c.name)
			}
		}
		if phase != "cold" && len(github.Requests())+len(registry.Requests()) != before {
			t.Fatal("fresh warm/offline snapshot resolution contacted an upstream")
		}
		for _, q := range github.Requests() {
			if q.Header.Get("Authorization") != "" {
				t.Fatal("GitHub/codeload snapshot fixture received a downstream credential")
			}
			if strings.Contains(q.Path, "/lfs/") || strings.Contains(q.Path, "/vendor/") {
				t.Fatal("snapshot attempted LFS hydration or submodule retrieval")
			}
		}
		negative := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(negative, "main.tf"),
			fmt.Appendf(nil, "module \"missing\" {\n source = %q\n version = \"99.0.1\"\n}\n", host+"/"+readRepo+"__acme/snapshots/aws"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		negativeEnv := append([]string(nil), env...)
		negativeEnv = append(negativeEnv, "HOME="+negative, "TMPDIR="+negative)
		out, err := w.terraformGetterCommand(program, negative, negativeEnv, "init", "-backend=false", "-input=false", "-no-color")
		missing := bytes.Contains(out, []byte("Unreadable module directory")) || bytes.Contains(out, []byte("Local module path escapes module package"))
		if err == nil || !missing || !bytes.Contains(out, []byte("vendor")) {
			t.Fatal("missing snapshot submodule did not fail as an absent module directory")
		}
		if err := w.serverAnswered(204, "GET", "/artifactory/api/terraform/v1/modules/"+readRepo+"__acme/snapshots/aws/99.0.1/download"); err != nil {
			t.Fatal(err)
		}
		for _, q := range github.Requests() {
			if strings.Contains(q.Path, "/lfs/") || strings.Contains(q.Path, "/vendor/") {
				t.Fatal("missing submodule induced an auxiliary source request")
			}
		}
		t.Logf(
			"NATIVE_SNAPSHOT version=%s backend=%s selection=%s phase=%s sources=%d discovery=%s refIdentity=PASS originalBytes=PASS",
			version,
			backend,
			readRepo,
			phase,
			len(cases),
			discovery,
		)
	}
}
