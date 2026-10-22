//go:build e2e

package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (p *upstreamProbe) nativeDockerConfig(dir string) string {
	config := filepath.Join(dir, "registry.json")
	host := strings.TrimPrefix(p.w.url, "http://")
	data, _ := json.Marshal(
		map[string]any{
			"auths": map[string]any{
				host: map[string]string{
					"auth": base64.StdEncoding.EncodeToString([]byte("admin:" + testAdminPassword)),
				},
			},
		},
	)
	if os.WriteFile(config, data, 0o600) != nil {
		p.fail("native-client", "local_configuration", "write isolated registry credentials")
	}
	return config
}

func (p *upstreamProbe) nativeHelm() {
	dir := p.nativeHome()
	program := os.Getenv("ARTIFACTWEAVE_HELM_BIN")
	env := []string{
		"HOME=" + dir,
		"HELM_CACHE_HOME=" + filepath.Join(dir, "cache"),
		"HELM_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"HELM_DATA_HOME=" + filepath.Join(dir, "data"),
		"HELM_REGISTRY_CONFIG=" + p.nativeDockerConfig(dir),
	}
	if p.phase == "cold" {
		version := p.nativeCommand("helm", program, []string{"version", "--short"}, env, dir)
		p.version("helm", version)
	}
	args := []string{
		"pull",
		"podinfo",
		"--repo",
		p.w.url + "/artifactory/api/helm/" + p.repo(),
		"--version",
		"6.7.1",
		"--username",
		"admin",
		"--password",
		testAdminPassword,
		"--destination",
		dir,
	}
	digest := p.pin.SHA256
	if p.family == "helmoci" {
		args = []string{
			"pull",
			"oci://" + strings.TrimPrefix(
				p.w.url,
				"http://",
			) + "/" + p.repo() + "/" + p.pin.Repository,
			"--version",
			p.pin.Tag,
			"--plain-http",
			"--destination",
			dir,
		}
		var doc struct {
			Manifest struct {
				Layers []struct {
					Digest string `json:"digest"`
				} `json:"layers"`
			} `json:"manifest"`
		}
		p.decode(p.pins["helmoci"], &doc)
		digest = strings.TrimPrefix(doc.Manifest.Layers[0].Digest, "sha256:")
	}
	p.nativeCommand("helm pull", program, args, env, dir)
	p.assertFile(dir, "podinfo-6.7.1.tgz", digest)
}

func (p *upstreamProbe) nativeOCI() {
	dir := p.nativeHome()
	program := os.Getenv("ARTIFACTWEAVE_CRANE_BIN")
	config := p.nativeDockerConfig(dir)
	if os.Rename(config, filepath.Join(dir, "config.json")) != nil {
		p.fail("native-client", "local_configuration", "prepare isolated OCI credentials")
	}
	env := []string{"HOME=" + dir, "DOCKER_CONFIG=" + dir}
	if p.phase == "cold" {
		version := p.nativeCommand("crane", program, []string{"version"}, env, dir)
		p.version("crane", version)
	}
	reference := strings.TrimPrefix(
		p.w.url,
		"http://",
	) + "/" + p.repo() + "/" + p.pin.Repository + "@" + p.pin.Digest
	p.nativeCommand(
		"crane pull",
		program,
		[]string{
			"pull",
			"--insecure",
			"--format",
			"oci",
			"--platform",
			"linux/amd64",
			reference,
			filepath.Join(dir, "image.tar"),
		},
		env,
		dir,
	)
	if info, err := os.Stat(
		filepath.Join(dir, "image.tar"),
	); err != nil || info.Size() == 0 ||
		info.Size() > 8<<20 {
		p.fail(
			"native-artifact",
			"client_assertion",
			"native OCI archive absent or outside size budget",
		)
	}
}

func (p *upstreamProbe) nativeTerraform() {
	dir := p.nativeHome()
	program := os.Getenv("ARTIFACTWEAVE_TERRAFORM_BIN")
	if p.w.vars["e2e-token"] == "" {
		data, _ := p.request(
			"local-credentials",
			http.MethodPost,
			"/access/api/v1/tokens",
			[]byte(`{"expires_in":900}`),
			"",
			200,
			"",
		)
		var token struct {
			AccessToken string `json:"access_token"`
		}
		p.decode(data, &token)
		if token.AccessToken == "" {
			p.fail("native-client", "local_configuration", "local test access token absent")
		}
		p.w.vars["e2e-token"] = token.AccessToken
	}
	host := strings.TrimPrefix(p.w.url, "http://")
	config := fmt.Sprintf(
		"host %q {\n services = {\n \"modules.v1\" = %q\n \"providers.v1\" = %q\n }\n}\ncredentials %q { token = %q }\n",
		host,
		p.w.url+"/artifactory/api/terraform/v1/modules/",
		p.w.url+"/artifactory/api/terraform/v1/providers/",
		host,
		p.w.vars["e2e-token"],
	)
	main := fmt.Sprintf(
		"module \"label\" {\n source = %q\n version = \"0.25.0\"\n}\n",
		host+"/"+p.repo()+"__cloudposse/label/null",
	)
	if p.family == "terraform-provider" {
		main = fmt.Sprintf(
			"terraform {\n required_providers {\n null = { source = %q, version = \"3.3.2\" }\n }\n}\n",
			host+"/tf-providers-rt-ns-hashicorp/null",
		)
	}
	for name, data := range map[string]string{".terraformrc": config, "main.tf": main} {
		if os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600) != nil {
			p.fail("native-client", "local_configuration", "write isolated Terraform configuration")
		}
	}
	env := []string{
		"HOME=" + dir,
		"TF_CLI_CONFIG_FILE=" + filepath.Join(dir, ".terraformrc"),
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"TF_REGISTRY_DISCOVERY_RETRY=0",
		"TF_REGISTRY_CLIENT_TIMEOUT=15",
		"CHECKPOINT_DISABLE=1",
	}
	if p.phase == "cold" {
		version := p.nativeCommand("terraform", program, []string{"version", "-json"}, env, dir)
		var v struct {
			Version string `json:"terraform_version"`
		}
		p.decode([]byte(version), &v)
		p.version("terraform", v.Version)
	}
	p.nativeCommand(
		"terraform init",
		program,
		[]string{"init", "-backend=false", "-input=false", "-no-color"},
		env,
		dir,
	)
	if p.family == "terraform-provider" {
		data, err := os.ReadFile(filepath.Join(dir, ".terraform.lock.hcl"))
		if err != nil || !strings.Contains(string(data), "h1:") {
			p.fail("native-artifact", "client_assertion", "provider lockfile content hash absent")
		}
	} else {
		data, err := os.ReadFile(filepath.Join(dir, ".terraform/modules/modules.json"))
		if err != nil || !strings.Contains(string(data), "0.25.0") {
			p.fail("native-artifact", "client_assertion", "pinned installed module version absent")
		}
	}
}

func (p *upstreamProbe) version(client, value string) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\x00") {
		p.fail("client-version", "local_configuration", "unexpected client version output")
	}
	p.record(
		upstreamEvent{
			Category:  "client-version",
			Outcome:   "pass",
			Client:    client,
			Assertion: value,
		},
	)
}
