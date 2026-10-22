//go:build e2e

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type upstreamPin struct {
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	Digest     string `json:"digest"`
	Registry   string `json:"registry"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Filename   string `json:"filename"`
}

type upstreamProbe struct {
	t             *testing.T
	w             *world
	ctx           context.Context
	report        *upstreamReport
	reportDir     string
	family, phase string
	pin           upstreamPin
	pins          map[string]json.RawMessage
	requests      *atomic.Int64
}

func TestRealUpstreams(t *testing.T) {
	if os.Getenv("ARTIFACTWEAVE_E2E") != "1" {
		t.Fatal("set ARTIFACTWEAVE_E2E=1 to authorize the bounded public read-only probes")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("the pinned conformance matrix requires linux/amd64")
	}
	reportDir := os.Getenv("ARTIFACTWEAVE_E2E_REPORT_DIR")
	if reportDir == "" {
		t.Fatal("ARTIFACTWEAVE_E2E_REPORT_DIR is required; outcomes must be retained")
	}
	data, err := os.ReadFile("testdata/upstreams/pins.json")
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]json.RawMessage{}
	if err = json.Unmarshal(data, &pins); err != nil {
		t.Fatal(err)
	}
	report := &upstreamReport{
		Revision:   os.Getenv("GITHUB_SHA"),
		HTTPClient: "Go net/http " + runtime.Version(),
		RunURL:     os.Getenv("ARTIFACTWEAVE_E2E_RUN_URL"),
	}
	defer func() {
		if err := writeUpstreamReport(reportDir, *report); err != nil {
			t.Error(err)
		}
	}()
	families := []string{
		"generic",
		"maven",
		"ivy",
		"gradle",
		"sbt",
		"npm",
		"pypi",
		"nuget-v3",
		"nuget-v2",
		"helm",
		"docker",
		"oci",
		"helmoci",
		"terraform-module",
		"terraform-provider",
	}
	selected := os.Getenv("ARTIFACTWEAVE_E2E_FAMILY")
	if selected == "" {
		selected = "all"
	}
	valid := selected == "all"
	for _, family := range families {
		if selected == family {
			valid = true
		}
	}
	if !valid {
		t.Fatal("unknown ARTIFACTWEAVE_E2E_FAMILY")
	}
	for _, family := range families {
		outcome := "pending"
		if selected != "all" && selected != family {
			outcome = "not_selected"
		}
		report.Events = append(
			report.Events,
			upstreamEvent{
				Family:    family,
				Phase:     "scenario",
				Category:  "scenario",
				Outcome:   outcome,
				Assertion: "explicit family selection",
			},
		)
	}
	if err = writeUpstreamReport(reportDir, *report); err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if selected != "all" && selected != family {
			continue
		}
		t.Run(family, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			p := &upstreamProbe{
				t:         t,
				ctx:       ctx,
				report:    report,
				reportDir: reportDir,
				family:    family,
				phase:     "setup",
				pins:      pins,
				requests:  new(atomic.Int64),
			}
			defer func() {
				outcome := "pass"
				if t.Failed() {
					outcome = "fail"
				}
				p.record(
					upstreamEvent{
						Category: "scenario",
						Outcome:  outcome,
						Assertion: fmt.Sprintf(
							"%d local requests; ceiling 160; no assertion retries",
							p.requests.Load(),
						),
					},
				)
			}()
			p.start()
			p.configure()
			for _, phase := range []string{"cold", "warm"} {
				p.phase = phase
				p.native()
				p.protocol()
			}
		})
	}
}

func (p *upstreamProbe) repo() string {
	if p.family == "terraform-provider" {
		return "tf-providers"
	}
	return "e2e-" + p.family
}

// A private gateway bounds all client and protocol requests, including rewritten
// URLs. The real binary advertises this gateway as its base URL, so native
// clients cannot evade the count by following returned repository links.
func (p *upstreamProbe) start() {
	port, err := freePort()
	if err != nil {
		p.fail("setup", "local_configuration", "allocate listener")
	}
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
	router := chi.NewRouter()
	router.Handle("/*", upstreamRequestBudget(proxy, p.requests, 160))
	front := httptest.NewServer(router)
	p.t.Cleanup(front.Close)
	w := newWorld(p.t)
	p.w = w
	w.url = front.URL
	w.dataDir = p.t.TempDir()
	w.vars = map[string]string{}
	w.server = exec.CommandContext(
		p.ctx,
		binary,
		"serve",
		"--listen",
		target.Host,
		"--data-dir",
		w.dataDir,
		"--base-url",
		w.url,
		"--log-format",
		"json",
	)
	w.server.Env = append(
		environWithout("ARTIFACTWEAVE_"),
		"ARTIFACTWEAVE_MASTER_KEY="+testMasterKey,
		"ARTIFACTWEAVE_BOOTSTRAP_ADMIN_PASSWORD="+testAdminPassword,
		"ARTIFACTWEAVE_TOKEN_ALLOW_BASIC_AUTH=true",
		"ARTIFACTWEAVE_PYPI_SIMPLE_JSON=true",
		"ARTIFACTWEAVE_PYPI_CORE_METADATA=true",
	)
	w.server.Stderr = &boundedUpstreamOutput{limit: 32 << 10}
	if err = w.server.Start(); err != nil {
		p.fail("setup", "local_configuration", "start server")
	}
	p.t.Cleanup(w.stop)
	if !p.waitReady() {
		p.fail("setup", "local_configuration", "server readiness")
	}
}

// Readiness polling is setup only, with a deadline and per-request timeout;
// it never retries a conformance assertion or consumes an unbounded request.
func (p *upstreamProbe) waitReady() bool {
	ctx, cancel := context.WithTimeout(p.ctx, 20*time.Second)
	defer cancel()
	client := http.Client{Timeout: time.Second}
	for {
		req, _ := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			p.w.url+"/artifactory/api/v1/system/readiness",
			nil,
		)
		response, err := client.Do(req)
		if response != nil {
			_ = response.Body.Close()
			if err == nil && response.StatusCode == 200 {
				return true
			}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (p *upstreamProbe) record(e upstreamEvent) {
	e.Family = p.family
	e.Phase = p.phase
	p.report.Events = append(p.report.Events, e)
	if err := writeUpstreamReport(p.reportDir, *p.report); err != nil {
		p.t.Fatal(err)
	}
	p.t.Logf(
		"CONFORMANCE family=%s phase=%s category=%s outcome=%s assertion=%s",
		e.Family,
		e.Phase,
		e.Category,
		e.Outcome,
		e.Assertion,
	)
}

func (p *upstreamProbe) fail(category, outcome, assertion string) {
	p.record(upstreamEvent{Category: category, Outcome: outcome, Assertion: assertion})
	p.t.Fatalf("%s %s: %s", category, outcome, assertion)
}

func (p *upstreamProbe) decode(data []byte, out any) {
	if json.Unmarshal(data, out) != nil {
		p.fail("metadata", "protocol_assertion", "response is not valid expected JSON")
	}
}

func (p *upstreamProbe) pinned(name string) upstreamPin {
	var pin upstreamPin
	p.decode(p.pins[name], &pin)
	return pin
}

func (p *upstreamProbe) request(
	category, method, target string,
	body []byte,
	accept string,
	want int,
	digest string,
) ([]byte, http.Header) {
	link, err := localUpstreamLink(p.w.url, target)
	if err != nil {
		p.fail(category, "protocol_assertion", "rewritten link bypasses the repository")
	}
	req, err := http.NewRequestWithContext(p.ctx, method, link, bytes.NewReader(body))
	if err != nil {
		p.fail(category, "local_configuration", "request construction")
	}
	req.SetBasicAuth("admin", testAdminPassword)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	client := http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return fmt.Errorf("redirect bound")
			}
			_, e := localUpstreamLink(p.w.url, r.URL.String())
			return e
		},
	}
	response, err := client.Do(req)
	if err != nil {
		p.diagnose(category)
		p.fail(category, "retrieval_failure", "bounded repository request did not complete")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, upstreamBodyLimit+1))
	if err != nil || len(data) > upstreamBodyLimit {
		p.fail(category, "protocol_assertion", "response body incomplete or exceeds 16 MiB")
	}
	if response.StatusCode != want {
		p.record(
			upstreamEvent{
				Category:  category,
				Outcome:   "protocol_assertion",
				Status:    response.StatusCode,
				Assertion: fmt.Sprintf("expected HTTP %d", want),
			},
		)
		p.diagnose(category)
		p.t.Fatalf("%s HTTP %d, expected %d", category, response.StatusCode, want)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if digest != "" && actual != digest {
		p.fail(category, "protocol_assertion", "SHA-256 differs from immutable fixture pin")
	}
	p.record(
		upstreamEvent{
			Category:  category,
			Outcome:   "pass",
			Status:    response.StatusCode,
			Bytes:     len(data),
			SHA256:    actual,
			Assertion: "status and bounded representation",
		},
	)
	return data, response.Header
}

// This independent single read never retries the failed contract. It identifies
// public endpoint outages/rate limits without claiming to locate every upstream
// hop behind a sanitized adapter error.
func (p *upstreamProbe) diagnose(category string) {
	if p.phase == "offline" || p.pin.URL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, p.pin.URL, nil)
	if err != nil {
		return
	}
	response, err := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return fmt.Errorf("diagnostic redirect bound")
		}
		return nil
	}}).Do(
		req,
	)
	status := 0
	if response != nil {
		status = response.StatusCode
		_ = response.Body.Close()
	}
	p.record(
		upstreamEvent{
			Category:  category + "-independent-upstream",
			Outcome:   upstreamStatus(status, err != nil),
			Status:    status,
			Assertion: "one diagnostic HEAD; original assertion remains failed",
		},
	)
}

func (p *upstreamProbe) configure() {
	family := p.family
	config := map[string]any{
		"rclass":                         "remote",
		"packageType":                    family,
		"hardFail":                       true,
		"socketTimeoutMillis":            15000,
		"metadataRetrievalTimeoutSecs":   20,
		"retrievalCachePeriodSecs":       3600,
		"missedRetrievalCachePeriodSecs": 0,
	}
	switch family {
	case "generic":
		p.pin = p.pinned("generic")
		config["url"] = "https://raw.githubusercontent.com/hashicorp/terraform/v1.16.4"
	case "maven", "ivy", "gradle", "sbt":
		p.pin = p.pinned("maven")
		config["url"] = "https://repo.maven.apache.org/maven2"
		config["repoLayoutRef"] = "maven-2-default"
		config["remoteRepoLayoutRef"] = "maven-2-default"
	case "npm":
		p.pin = p.pinned("npm")
		config["url"] = "https://registry.npmjs.org"
	case "pypi":
		var files []upstreamPin
		p.decode(p.pins["pypi"], &files)
		p.pin = files[0]
		config["url"] = "https://pypi.org"
		config["pyPIRegistryUrl"] = "https://pypi.org"
		config["pyPIRepositorySuffix"] = "simple"
		config["externalDependenciesEnabled"] = true
		config["externalDependenciesPatterns"] = []string{"https://files.pythonhosted.org/**"}
	case "nuget-v3", "nuget-v2":
		p.pin = p.pinned("nuget")
		config["packageType"] = "nuget"
		config["url"] = "https://api.nuget.org/v3/index.json"
		config["v3FeedUrl"] = "https://api.nuget.org/v3/index.json"
		if family == "nuget-v2" {
			config["url"] = "https://www.nuget.org/api/v2"
			delete(config, "v3FeedUrl")
		}
		config["externalDependenciesEnabled"] = true
		config["externalDependenciesPatterns"] = []string{
			"https://api.nuget.org/**",
			"https://*.nuget.org/**",
		}
	case "helm":
		p.pin = p.pinned("helm")
		config["url"] = "https://stefanprodan.github.io/podinfo"
	case "docker", "oci", "helmoci":
		name := "docker"
		if family == "helmoci" {
			name = "helmoci"
		}
		p.pin = p.pinned(name)
		config["url"] = p.pin.Registry
		config["enableTokenAuthentication"] = true
		p.pin.URL = p.pin.Registry + "/v2/"
	case "terraform-module":
		p.pin = p.pinned("module-archive")
		config["packageType"] = "terraform"
		config["url"] = "https://github.com/cloudposse/terraform-null-label"
		config["terraformRegistryUrl"] = "https://registry.terraform.io"
	case "terraform-provider":
		p.pin = p.pinned("terraform-provider")
		config["packageType"] = "terraform"
		config["url"] = "https://registry.terraform.io"
		config["terraformRegistryUrl"] = "https://registry.terraform.io"
		config["terraformProvidersUrl"] = "https://releases.hashicorp.com"
	}
	body, _ := json.Marshal(config)
	p.request(
		"configuration",
		http.MethodPut,
		"/artifactory/api/repositories/"+p.repo(),
		body,
		"",
		200,
		"",
	)
}

func (p *upstreamProbe) nativeCommand(
	client, program string,
	args, env []string,
	dir string,
) string {
	if program == "" {
		p.fail(
			"native-client",
			"local_configuration",
			client+" is required, never silently skipped",
		)
	}
	ctx, cancel := context.WithTimeout(p.ctx, 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, program, args...)
	command.Dir = dir
	command.Env = append(environWithout("TF_"), env...)
	output := &boundedUpstreamOutput{limit: 32 << 10}
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		p.diagnose("native-client")
		detail := upstreamDiagnostic(
			output.String(),
			testAdminPassword,
			testMasterKey,
			p.w.vars["e2e-token"],
		)
		code := -1
		if command.ProcessState != nil {
			code = command.ProcessState.ExitCode()
		}
		p.record(
			upstreamEvent{
				Category: "native-client",
				Outcome:  "client_assertion",
				Client:   client,
				Assertion: fmt.Sprintf(
					"command exit %d; deadline exceeded %t",
					code,
					ctx.Err() != nil,
				),
				Detail: detail,
			},
		)
		p.t.Fatalf(
			"%s native command failed (bounded sanitized diagnostic retained in outcomes.json)",
			client,
		)
	}
	p.record(
		upstreamEvent{
			Category:  "native-client",
			Outcome:   "pass",
			Assertion: "bounded native command completed",
			Client:    client,
		},
	)
	return output.String()
}

func (p *upstreamProbe) native() {
	switch p.family {
	case "helm", "helmoci":
		p.nativeHelm()
	case "docker", "oci":
		p.nativeOCI()
	case "terraform-provider", "terraform-module":
		p.nativeTerraform()
	default:
		p.record(
			upstreamEvent{
				Category:  "evidence-kind",
				Outcome:   "protocol_only",
				Assertion: "Go HTTP protocol/digest proof; native matrix remains controlled acceptance",
			},
		)
	}
}

func (p *upstreamProbe) nativeHome() string { dir := p.t.TempDir(); p.w.dir = dir; return dir }
func (p *upstreamProbe) assertFile(dir, name, digest string) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		p.fail("native-artifact", "client_assertion", "native artifact missing")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		p.fail(
			"native-artifact",
			"client_assertion",
			"native artifact differs from immutable SHA-256",
		)
	}
	p.record(
		upstreamEvent{
			Category:  "native-artifact",
			Outcome:   "pass",
			Bytes:     len(data),
			SHA256:    digest,
			Assertion: "native artifact matches pin",
		},
	)
}
