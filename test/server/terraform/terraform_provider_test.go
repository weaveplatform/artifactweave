package terraform_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
)

func TestAccTerraformProviderInstall(t *testing.T) {
	binary, archivePath := os.Getenv(
		"ARTIFACTWEAVE_TERRAFORM_BIN",
	), os.Getenv(
		"ARTIFACTWEAVE_NULL_PROVIDER_ZIP",
	)
	if binary == "" || archivePath == "" {
		t.Skip(
			"set ARTIFACTWEAVE_TERRAFORM_BIN and ARTIFACTWEAVE_NULL_PROVIDER_ZIP for pinned native installation",
		)
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	h := statusFixture(
		t,
		false,
		allowBasicTokens,
		func(c *Config) { c.BaseURL = ""; tuneRegistry(c) },
	)
	body, _ := json.Marshal(
		map[string]string{
			"alias": "tf-sign", "pairName": "tf-sign",
			"pairType":   "GPG",
			"publicKey":  key.Public,
			"privateKey": key.Private,
		},
	)
	wantStatus(
		t,
		do(
			t,
			h,
			"POST",
			"/artifactory/api/security/keypair",
			jsonAs(basic("admin", "pw")),
			string(body),
		),
		201,
	)
	for _, repoKey := range []string{"providers", "mirror"} {
		body := `{"rclass":"local","packageType":"terraform","terraformType":"PROVIDER"}`
		if repoKey == "providers" {
			body = `{"rclass":"local","packageType":"terraform","terraformType":"PROVIDER","primaryKeyPairRef":"tf-sign"}`
		}
		wantStatus(
			t,
			do(
				t,
				h,
				"PUT",
				"/artifactory/api/repositories/"+repoKey,
				jsonAs(basic("admin", "pw")),
				body,
			),
			200,
		)
	}
	rel := terraformtest.Release{Type: "null", Version: "3.3.2"}
	archiveName := rel.Name(runtime.GOOS + "_" + runtime.GOARCH + ".zip")
	digest := sha256.Sum256(archive)
	sums := hex.EncodeToString(digest[:]) + "  " + archiveName + "\n"
	for _, repoKey := range []string{"providers", "mirror"} {
		files := map[string][]byte{archiveName: archive}
		if repoKey == "providers" {
			files[rel.Name("SHA256SUMS")] = []byte(sums)
			files[rel.Name("SHA256SUMS.sig")] = key.Sign([]byte(sums))
		}
		for name, data := range files {
			wantStatus(
				t,
				do(
					t,
					h,
					"PUT",
					"/artifactory/"+repoKey+"/hashicorp/null/3.3.2/"+name,
					basic("admin", "pw"),
					string(data),
				),
				201,
			)
		}
	}
	token, _ := mintAdmin(t, h, `{"expires_in":3600}`)["access_token"].(string)
	server := httptest.NewServer(h)
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(r *http.Request) { original(r); r.Header.Set("X-JFrog-Override-Base-Url", "https://"+r.Host) }
	front := httptest.NewTLSServer(proxy)
	defer front.Close()
	for _, mode := range []string{"registry", "mirror", "anonymous", "bad-signature", "bad-checksum"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "mirror" && runtime.GOOS != "linux" {
				t.Skip(
					"pinned Terraform Go1.26 cannot trust SSL_CERT_FILE on macOS/Windows; Linux acceptance exercises mirror",
				)
			}
			dir := t.TempDir()
			write := func(name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			source := target.Host + "/providers-rt-ns-hashicorp/null"
			config := fmt.Sprintf(
				"host %q {\nservices={\n\"providers.v1\"=%q\n}\n}\n",
				target.Host,
				server.URL+"/artifactory/api/terraform/v1/providers/",
			)
			authHost := target.Host
			if mode == "mirror" {
				source = "hashicorp/null"
				authHost = strings.TrimPrefix(front.URL, "https://")
				config = fmt.Sprintf(
					"provider_installation {\nnetwork_mirror {\nurl=%q\n}\n}\n",
					front.URL+"/artifactory/api/terraform/mirror/providers/",
				)
			}
			if mode != "anonymous" {
				config += fmt.Sprintf("credentials %q { token=%q }\n", authHost, token)
			}
			write("cli.tfrc", config)
			write(
				"main.tf",
				fmt.Sprintf(
					"terraform {\nrequired_providers {\nnull={source=%q,version=\"3.3.2\"}\n}\n}\n",
					source,
				),
			)
			write(
				"ca.pem",
				string(
					pem.EncodeToMemory(
						&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw},
					),
				),
			)
			if strings.HasPrefix(mode, "bad-") {
				caseSums := sums
				if mode == "bad-checksum" {
					caseSums = strings.Repeat("0", 64) + "  " + archiveName + "\n"
				}
				caseSignature := key.Sign([]byte(caseSums))
				if mode == "bad-signature" {
					caseSignature = []byte("broken")
				}
				for suffix, data := range map[string]string{
					"SHA256SUMS":     caseSums,
					"SHA256SUMS.sig": string(caseSignature),
				} {
					wantStatus(t, do(t, h, "PUT",
						"/artifactory/providers/hashicorp/null/3.3.2/"+rel.Name(suffix),
						basic("admin", "pw"), data), http.StatusCreated)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(
				ctx,
				binary,
				"init",
				"-backend=false",
				"-input=false",
				"-no-color",
			)
			cmd.Dir = dir
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "TF_") && !strings.HasPrefix(kv, "HOME=") {
					cmd.Env = append(cmd.Env, kv)
				}
			}
			cmd.Env = append(
				cmd.Env,
				"HOME="+dir,
				"TF_CLI_CONFIG_FILE="+filepath.Join(dir, "cli.tfrc"),
				"TF_IN_AUTOMATION=1",
				"CHECKPOINT_DISABLE=1",
				"SSL_CERT_FILE="+filepath.Join(dir, "ca.pem"),
			)
			out, err := cmd.CombinedOutput()
			if mode == "anonymous" || strings.HasPrefix(mode, "bad-") {
				want := "409 Conflict"
				if mode == "anonymous" {
					want = "requires authentication credentials"
					wantStatus(t, do(
						t,
						h,
						"GET",
						"/artifactory/api/terraform/v1/providers/providers-rt-ns-hashicorp/null/versions",
						nil,
						"",
					), http.StatusUnauthorized)
				}
				message := strings.Join(strings.Fields(string(out)), " ")
				if err == nil || !strings.Contains(message, want) {
					t.Fatalf("init %s: %v\n%s", mode, err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("init %s: %v\n%s", mode, err, out)
			}
			lock, err := os.ReadFile(filepath.Join(dir, ".terraform.lock.hcl"))
			if err != nil || !strings.Contains(string(lock), "h1:") {
				t.Fatalf("lock: %s %v", lock, err)
			}
			if mode == "registry" && !strings.Contains(string(out), key.KeyID()) {
				t.Fatalf("signature identity absent: %s", out)
			}
		})
	}
}
