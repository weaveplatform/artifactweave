package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

const backendAPI = "/artifactory/api/terraform/remote/v2/"

func (w *world) registerTerraformBackend(sc *godog.ScenarioContext) {
	sc.Step(
		`^a Terraform remote backend with (name|prefix) "([^"]*)" and encryption (enabled|disabled)$`,
		w.configureBackend,
	)
	sc.Step(`^the remote configuration uses the private null provider$`, w.backendProvider)
	sc.Step(
		`^Terraform's pulled state matches the latest version and is (encrypted|plain) through REST$`,
		w.backendStateMatches,
	)
	sc.Step(`^the next apply changes the null resource$`, w.backendChange)
	sc.Step(`^the backend has advanced its state version$`, w.backendAdvanced)
	sc.Step(
		`^a second Terraform working copy cannot plan while the first holds the lock$`,
		w.backendContention,
	)
	sc.Step(`^a Terraform state of 21 MiB is ready to push$`, w.backendLargeState)
	sc.Step(`^Terraform logs in with the issued token$`, w.backendLogin)
	sc.Step(`^Terraform uses the kept backend token "([^"]*)"$`, func(name string) error {
		return w.backendCredentials(w.vars[name])
	})
}

func (w *world) backendRequest(method, path, body string, status int) error {
	if err := w.send(method, path, body, func(r *http.Request) {
		r.SetBasicAuth("admin", testAdminPassword)
	}); err != nil {
		return err
	}
	return w.statusIs(status)
}

func (w *world) configureBackend(kind, name, encryption string) error {
	if err := w.backendRequest(
		"PUT",
		"/artifactory/api/repositories/tf-state",
		fmt.Sprintf(
			`{"rclass":"local","packageType":"terraformbackend","encryptStates":%t}`,
			encryption == "enabled",
		),
		200,
	); err != nil {
		return err
	}
	if err := w.backendRequest(
		"POST",
		"/access/api/v1/tokens",
		`{"expires_in":3600}`,
		200,
	); err != nil {
		return err
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(w.body, &token); err != nil {
		return err
	}
	if w.vars == nil {
		w.vars = map[string]string{}
	}
	w.vars["backend-token"] = token.AccessToken
	w.vars["backend-host"] = strings.TrimPrefix(w.url, "http://")
	// Copy the real discovery document: Terraform fetches discovery only over
	// HTTPS, but supports HTTP service URLs in an explicit host configuration.
	if err := w.get("/.well-known/terraform.json"); err != nil {
		return err
	}
	if err := w.statusIs(200); err != nil {
		return err
	}
	var services map[string]string
	if err := json.Unmarshal(w.body, &services); err != nil {
		return err
	}
	var config strings.Builder
	fmt.Fprintf(&config, "host %q {\n services = {\n", w.vars["backend-host"])
	keys := make([]string, 0, len(services))
	for key := range services {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&config, "  %q = %q\n", key, services[key])
	}
	config.WriteString(" }\n}\n")
	if err := w.writeFile(".terraformrc", &godog.DocString{Content: config.String()}); err != nil {
		return err
	}
	if err := w.backendCredentials(token.AccessToken); err != nil {
		return err
	}
	w.vars["backend-config"] = fmt.Sprintf(
		"terraform {\n backend \"remote\" {\n hostname = %q\n organization = \"tf-state\"\n workspaces { %s = %q }\n }\n}\n",
		w.vars["backend-host"],
		kind,
		name,
	)
	return w.writeFile("backend.tf", &godog.DocString{Content: w.vars["backend-config"]})
}

func (w *world) backendCredentials(token string) error {
	credentials, err := json.Marshal(map[string]any{"credentials": map[string]any{
		w.vars["backend-host"]: map[string]string{"token": token},
	}})
	if err != nil {
		return err
	}
	return w.writeFile(
		".terraform.d/credentials.tfrc.json",
		&godog.DocString{Content: string(credentials)},
	)
}

func (w *world) backendProvider() error {
	return w.writeFile("main.tf", &godog.DocString{Content: fmt.Sprintf(`terraform {
  required_providers {
    null = { source = %q, version = "3.3.2" }
  }
}
variable "revision" { default = "first" }
resource "null_resource" "proof" { triggers = { revision = var.revision } }
`, w.vars["backend-host"]+"/tf-providers-rt-ns-hashicorp/null")})
}

func (w *world) backendChange() error {
	return w.writeFile("terraform.tfvars", &godog.DocString{Content: `revision = "second"`})
}

func (w *world) backendStateMatches(storage string) error {
	var pulled map[string]any
	if err := json.Unmarshal(w.body, &pulled); err != nil {
		return fmt.Errorf("decode terraform state pull: %w", err)
	}
	if err := w.backendRequest(
		"GET",
		backendAPI+"workspaces/tf-state__dev/current-state-version",
		"",
		200,
	); err != nil {
		return err
	}
	var current struct {
		Data struct {
			ID         string
			Attributes struct {
				Serial int
			}
		}
	}
	if err := json.Unmarshal(w.body, &current); err != nil {
		return err
	}
	if current.Data.ID == "" || pulled["serial"] != float64(current.Data.Attributes.Serial) {
		return fmt.Errorf("latest version does not identify the pulled serial: %s", w.body)
	}
	w.vars["previous-state-version"] = current.Data.ID
	if err := w.backendRequest(
		"GET",
		backendAPI+"state-versions/"+current.Data.ID+"/download",
		"",
		200,
	); err != nil {
		return err
	}
	plain := bytes.Clone(w.body)
	var stored map[string]any
	if err := json.Unmarshal(plain, &stored); err != nil {
		return err
	}
	// state pull may update the writer's Terraform version and JSON layout.
	for _, key := range []string{"version", "serial", "lineage", "resources", "outputs"} {
		if !reflect.DeepEqual(pulled[key], stored[key]) {
			return fmt.Errorf("state pull differs from the stored state in %s", key)
		}
	}
	if err := w.backendRequest(
		"GET",
		"/artifactory/tf-state/workspaces/dev/state.latest.json",
		"",
		200,
	); err != nil {
		return err
	}
	if storage == "plain" && !bytes.Equal(w.body, plain) {
		return fmt.Errorf("unencrypted item differs from the backend download")
	}
	if storage == "encrypted" &&
		(bytes.Equal(w.body, plain) || json.Valid(w.body) || bytes.Contains(w.body, []byte(`"lineage"`))) {
		return fmt.Errorf("encrypted item exposed state JSON")
	}
	return w.serverAnswered(200, "PUT", backendAPI+"state-versions/"+current.Data.ID+"/content")
}

func (w *world) backendAdvanced() error {
	if err := w.backendRequest(
		"GET",
		backendAPI+"state-versions?filter[organization][name]=tf-state&filter[workspace][name]=dev",
		"",
		200,
	); err != nil {
		return err
	}
	var versions struct {
		Data []struct {
			ID         string
			Attributes struct{ Serial int }
		}
	}
	if err := json.Unmarshal(w.body, &versions); err != nil {
		return err
	}
	if len(versions.Data) < 2 || versions.Data[0].ID == w.vars["previous-state-version"] ||
		versions.Data[1].ID != w.vars["previous-state-version"] ||
		versions.Data[0].Attributes.Serial <= versions.Data[1].Attributes.Serial {
		return fmt.Errorf("changed apply did not advance the latest version: %s", w.body)
	}
	return nil
}

func (w *world) backendCommand(ctx context.Context, sub string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Getenv(clientBinaries["terraform"]), args...)
	cmd.Dir = filepath.Join(w.dir, sub)
	// A TF_CLI_CONFIG_FILE override suppresses credentials.tfrc.json loading.
	// Use the isolated HOME's standard files so login and subsequent commands
	// exercise the credentials Terraform actually persists.
	cmd.Env = append(environWithout("TF_"), "HOME="+w.dir,
		"TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1")
	return cmd
}

func (w *world) backendContention() error {
	// The second working copy has independent backend metadata and provider
	// installation, so its lock failure cannot come from local file locking.
	for _, name := range []string{"backend.tf", "main.tf", "terraform.tfvars"} {
		body, err := os.ReadFile(filepath.Join(w.dir, name))
		if err != nil {
			return err
		}
		if err := w.writeFile("second/"+name, &godog.DocString{Content: string(body)}); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := w.runClient(w.backendCommand(ctx, "second", "init", "-no-color")); err != nil {
		return err
	}
	if err := w.commandSucceeds(); err != nil {
		return err
	}
	gate := filepath.Join(w.dir, "release-lock")
	command := fmt.Sprintf(
		"i=0; while [ ! -f '%s' ]; do i=$((i+1)); [ $i -lt 150 ] || exit 1; sleep 0.1; done",
		gate,
	)
	if err := w.writeFile(
		"hold.tf",
		&godog.DocString{
			Content: fmt.Sprintf(
				"resource \"terraform_data\" \"hold\" {\n provisioner \"local-exec\" { command = %q }\n}\n",
				command,
			),
		},
	); err != nil {
		return err
	}
	first := w.backendCommand(ctx, "", "apply", "-auto-approve", "-no-color")
	var output bytes.Buffer
	first.Stdout, first.Stderr = &output, &output
	if err := first.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- first.Wait() }()
	finished := false
	defer func() {
		_ = os.WriteFile(gate, nil, 0o600)
		if !finished {
			cancel()
			<-done
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := w.getAsAdmin(
			"/artifactory/api/storage/tf-state/workspaces/dev?properties",
		); err != nil {
			return err
		}
		if w.status != 404 {
			if err := w.statusIs(200); err != nil {
				return err
			}
			var properties struct{ Properties map[string][]string }
			if err := json.Unmarshal(w.body, &properties); err != nil {
				return err
			}
			if reflect.DeepEqual(properties.Properties["locked.by"], []string{"admin"}) {
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("first Terraform apply did not acquire the backend lock")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := w.runClient(
		w.backendCommand(ctx, "second", "plan", "-lock-timeout=0s", "-no-color"),
	); err != nil {
		return err
	}
	if err := w.commandFails(); err != nil {
		return err
	}
	if err := w.outputContains("tf-state/dev"); err != nil {
		return err
	}
	// Terraform prints its own lock info; the API's conflict names the holder.
	if err := w.backendRequest(
		"POST",
		backendAPI+"workspaces/tf-state__dev/actions/lock",
		"",
		409,
	); err != nil {
		return err
	}
	if err := w.bodyContains("admin"); err != nil {
		return err
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		return err
	}
	err := <-done
	finished = true
	if err != nil {
		return fmt.Errorf("lock-holding apply: %w\n%s", err, output.String())
	}
	return nil
}

func (w *world) backendLargeState() error {
	state, err := json.Marshal(map[string]any{
		"version": 4, "serial": 1, "lineage": "backend-large-state",
		"terraform_version": "1.16.4", "resources": []any{},
		"outputs": map[string]any{"large": map[string]any{
			"value": strings.Repeat("x", 21<<20), "type": "string", "sensitive": false,
		}},
	})
	if err != nil {
		return err
	}
	return w.writeFile("large.tfstate", &godog.DocString{Content: string(state)})
}

func (w *world) backendLogin() error {
	credentials := filepath.Join(w.dir, ".terraform.d", "credentials.tfrc.json")
	if err := os.Remove(credentials); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := w.backendCommand(ctx, "", "login", "-no-color", w.vars["backend-host"])
	cmd.Env = append(cmd.Env, "TF_INPUT=1", "BROWSER=true")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdin.Close()
	var output syncBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return err
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
	}()
	if _, err := io.WriteString(stdin, "yes\n"); err != nil {
		return err
	}
	// Terraform constructs a fresh buffered reader for each prompt. Sending
	// both lines together lets the first reader consume and discard the token.
	for !strings.Contains(output.String(), "Token for "+w.vars["backend-host"]+":") {
		if ctx.Err() != nil {
			return fmt.Errorf("terraform login did not request a token: %w", ctx.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := io.WriteString(stdin, w.vars["backend-token"]+"\n"); err != nil {
		return err
	}
	err = cmd.Wait()
	waited = true
	w.body, w.lastRC = []byte(output.String()), cmd.ProcessState.ExitCode()
	if err != nil {
		return fmt.Errorf("terraform login: %w\n%s", err, w.body)
	}
	if err := w.commandSucceeds(); err != nil {
		return err
	}
	if err := w.outputContains("Retrieved token for user admin"); err != nil {
		return err
	}
	contents, err := os.ReadFile(credentials)
	if err != nil {
		return err
	}
	var stored struct {
		Credentials map[string]struct{ Token string }
	}
	if err := json.Unmarshal(contents, &stored); err != nil {
		return err
	}
	if stored.Credentials[w.vars["backend-host"]].Token != w.vars["backend-token"] {
		return fmt.Errorf("terraform login did not store the checked token")
	}
	return w.serverAnswered(200, "GET", backendAPI+"account/details")
}
