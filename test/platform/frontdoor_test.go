package platform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/weaveplatform/artifactweave/pkg/blob/azure"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

// azure/cdn (#134): a storage account behind Azure Front Door Premium, which
// reaches it over Private Link. Front Door checks no signature; it passes the
// SAS through to storage. Before artifactweave starts, frontDoorReady
// approves the private endpoint connection Front Door asked for and waits
// for the edge and the storage firewall. The CDN checks of cdn_test.go then
// run with a SAS signer, and two checks follow that only a real Front Door
// can show: the route's cache cap ends an edge copy, and Private Link alone
// reaches the account once its public endpoint is off.

const (
	// frontDoorRollout bounds the wait for Front Door to bring a new
	// endpoint up everywhere and connect it over Private Link, which
	// Microsoft gives as some minutes after the approval.
	frontDoorRollout = 40 * time.Minute
	// firewallWait bounds the wait for the account's network rules to admit
	// or refuse a client after they change.
	firewallWait  = 5 * time.Minute
	frontDoorPoll = 20 * time.Second
)

func init() {
	scenarioBeforeStart["azure/cdn"] = frontDoorReady
	scenarioChecks["azure/cdn"] = append(
		cdnChecks(frontDoorSigner),
		check{
			"the edge stops serving a URL once its SAS and the cache cap have passed",
			edgeCapEndsCopies,
		},
		check{
			"only Private Link reaches the account once its public endpoint is off",
			privateLinkOnly,
		},
	)
}

// frontDoorConfig is what the checks read from the scenario's rendered
// configuration.
type frontDoorConfig struct {
	Blob struct {
		Azure struct {
			Account   string `yaml:"account"`
			Container string `yaml:"container"`
			Prefix    string `yaml:"prefix"`
			CDN       struct {
				FrontDoor struct {
					Endpoint   string `yaml:"endpoint"`
					PathPrefix string `yaml:"path_prefix"`
				} `yaml:"front_door"`
			} `yaml:"cdn"`
		} `yaml:"azure"`
	} `yaml:"blob"`
	Download struct {
		Redirect struct {
			TTL time.Duration `yaml:"ttl"`
		} `yaml:"redirect"`
	} `yaml:"download"`
}

func readFrontDoor(t *testing.T, h *harness) frontDoorConfig {
	t.Helper()
	var c frontDoorConfig
	readConfig(t, h, &c)
	if c.Blob.Azure.CDN.FrontDoor.Endpoint == "" || c.Download.Redirect.TTL <= 0 {
		t.Fatalf("the scenario's configuration names no Front Door endpoint or redirect TTL")
	}
	return c
}

// frontDoorReady waits, before artifactweave starts, for what a fresh
// instance lacks: the approval of Front Door's private endpoint connection,
// which the azurerm provider cannot give (the README's Prerequisites run the
// same az commands); the storage firewall admitting the runner, whose
// address the scenario allows; and the edge reaching the account.
func frontDoorReady(t *testing.T, h *harness) {
	t.Helper()
	c := readFrontDoor(t, h)
	deadline := time.Now().Add(frontDoorRollout)
	approvePrivateLink(t, h, deadline)
	waitForStorage(t, c, true, time.Now().Add(firewallWait))
	waitForEdge(t, c, deadline)
	waitForLargeDownloads(t, h, c, deadline)
}

// waitForLargeDownloads waits until a download of several of Front Door's
// 8 MiB chunks completes through the edge. The edge answering one request
// is not enough: in runs 38906963602 and 38916559470 the first chunk was
// served and the edge's fetch of the next was reset, minutes after
// waitForEdge returned, while the same download passed in 38925440868 once
// a few more minutes had gone by. Each attempt signs a new URL, so it
// misses the cache and fetches every chunk from the account.
func waitForLargeDownloads(t *testing.T, h *harness, c frontDoorConfig, deadline time.Time) {
	t.Helper()
	data := make([]byte, 20<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	// A content-addressed name under the route's pattern, as artifactweave
	// stores blobs, so the probe takes the path real downloads take.
	name := storePrefix(c.Blob.Azure.Prefix) + "sha256/" + want[:2] + "/" + want
	file := filepath.Join(t.TempDir(), "readiness.bin")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	blob := []string{
		"--account-name", c.Blob.Azure.Account, "--container-name", c.Blob.Azure.Container,
		"--name", name, "--auth-mode", "login", "--only-show-errors", "--output", "none",
	}
	cli(
		t,
		"az",
		append([]string{"storage", "blob", "upload", "--file", file, "--overwrite"}, blob...)...)
	client := &http.Client{Timeout: 5 * time.Minute}
	started, last := time.Now(), ""
	for {
		now := time.Now()
		u := frontDoorURL(t, h, c.Blob.Azure.Container+"/"+name, azure.ReadSAS{
			TTL: 10 * time.Minute, Start: now, Expiry: now.Add(10 * time.Minute),
			CacheControl: "public, max-age=600",
		})
		got, err := edgeDigest(t, client, u)
		if err == nil && got == want {
			t.Logf("downloads over 8 MiB complete through the edge after a further %s",
				time.Since(started).Round(time.Second))
			cli(t, "az", append([]string{"storage", "blob", "delete"}, blob...)...)
			return
		}
		last = fmt.Sprintf("%v (digest %.12s)", err, got)
		if time.Now().After(deadline) {
			t.Fatalf("a download over 8 MiB did not complete through the edge by %s; last: %s",
				deadline.Format(time.RFC3339), last)
		}
		time.Sleep(frontDoorPoll)
	}
}

// edgeDigest downloads u and returns the SHA-256 of what came back.
func edgeDigest(t *testing.T, client *http.Client, u *url.URL) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", resp.Status)
	}
	hsh := sha256.New()
	if _, err := io.Copy(hsh, resp.Body); err != nil {
		return hex.EncodeToString(hsh.Sum(nil)), err
	}
	return hex.EncodeToString(hsh.Sum(nil)), nil
}

// approvePrivateLink approves every pending private endpoint connection on
// the scenario's account, which only Front Door's can be, until one is
// approved.
func approvePrivateLink(t *testing.T, h *harness, deadline time.Time) {
	t.Helper()
	id := h.output(t, "storage_account_id")
	want := h.output(t, "private_link_request_message")
	for {
		// az prints the resource as the provider returns it, the state under
		// properties; some versions flatten it, so either is read.
		type state struct {
			Status      string `json:"status"`
			Description string `json:"description"`
		}
		var conns []struct {
			ID         string `json:"id"`
			State      state  `json:"privateLinkServiceConnectionState"`
			Properties struct {
				State state `json:"privateLinkServiceConnectionState"`
			} `json:"properties"`
		}
		out := cli(t, "az", "network", "private-endpoint-connection", "list", "--id", id,
			"--only-show-errors", "--output", "json")
		if err := json.Unmarshal([]byte(out), &conns); err != nil {
			t.Fatalf("parse the account's private endpoint connections: %v", err)
		}
		for _, pc := range conns {
			s := pc.Properties.State
			if s.Status == "" {
				s = pc.State
			}
			switch s.Status {
			case "Approved":
				t.Logf(
					"Front Door's private endpoint connection is approved (request %q)",
					s.Description,
				)
				return
			case "Pending":
				if s.Description != want {
					t.Logf("approving a pending connection whose request is %q, not %q: "+
						"nothing but Front Door connects to this scenario's account", s.Description, want)
				}
				cli(
					t,
					"az",
					"network",
					"private-endpoint-connection",
					"approve",
					"--id",
					pc.ID,
					"--description",
					"approved by test/platform",
					"--only-show-errors",
					"--output",
					"none",
				)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"Front Door's private endpoint connection was not approved by %s; connections: %s",
				deadline.Format(time.RFC3339),
				strings.TrimSpace(out),
			)
		}
		time.Sleep(frontDoorPoll)
	}
}

// waitForStorage waits until the account's public endpoint admits the
// runner (reachable) or refuses it (not reachable). The storage firewall
// refuses with 403 before it looks at the caller's identity, and the
// deployer holds blob rights on the account, so listing the container as
// the deployer tells the two apart.
func waitForStorage(t *testing.T, c frontDoorConfig, reachable bool, deadline time.Time) {
	t.Helper()
	api, err := azure.NewAPI(azure.ClientConfig{
		Account:   c.Blob.Azure.Account,
		Container: c.Blob.Azure.Container,
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		_, _, err := api.List(ctx, "readiness/", "")
		cancel()
		var re *azcore.ResponseError
		refused := errors.As(err, &re) && re.StatusCode == http.StatusForbidden
		if (reachable && err == nil) || (!reachable && refused) {
			t.Logf("the account's public endpoint answers the runner: %t", reachable)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"the account's public endpoint did not become reachable=%t from the runner by %s: %v",
				reachable,
				deadline.Format(time.RFC3339),
				err,
			)
		}
		time.Sleep(frontDoorPoll)
	}
}

// waitForEdge waits until a request for a blob path through the edge gets
// storage's answer: an unsigned request is refused by the account, with its
// x-ms headers, once the route is live everywhere it was asked and Private
// Link is connected. Before that, Front Door answers itself.
func waitForEdge(t *testing.T, c frontDoorConfig, deadline time.Time) {
	t.Helper()
	probe := &url.URL{
		Scheme: "https",
		Host:   c.Blob.Azure.CDN.FrontDoor.Endpoint,
		Path: "/" + c.Blob.Azure.Container + "/" + storePrefix(
			c.Blob.Azure.Prefix,
		) + "sha256/00/readiness",
	}
	client := &http.Client{Timeout: time.Minute}
	started, last := time.Now(), ""
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, probe.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if code := resp.Header.Get(
				"X-Ms-Error-Code",
			); code != "" ||
				resp.Header.Get("X-Ms-Request-Id") != "" {
				t.Logf("the edge reaches the account after %s: %s %s",
					time.Since(started).Round(time.Second), resp.Status, code)
				return
			}
			last = resp.Status + " from Front Door itself"
		} else {
			last = err.Error()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the edge did not reach the account by %s; last answer %s",
				deadline.Format(time.RFC3339), last)
		}
		time.Sleep(frontDoorPoll)
	}
}

// storePrefix normalises blob.azure.prefix as the backend does: no leading
// slash, one trailing slash.
func storePrefix(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// frontDoorSigner signs a URL for objectKey, "<container>/<blob name>", as
// artifactweave does for Front Door: a user delegation SAS minted as the
// deployer, as if at now, moved onto the endpoint.
func frontDoorSigner(t *testing.T, h *harness, objectKey string, now time.Time) *url.URL {
	t.Helper()
	return frontDoorURL(t, h, objectKey, azure.ReadSAS{
		TTL:          time.Minute,
		Start:        now,
		Expiry:       now.Add(time.Minute),
		CacheControl: "public, max-age=60",
	})
}

func frontDoorURL(t *testing.T, h *harness, objectKey string, rs azure.ReadSAS) *url.URL {
	t.Helper()
	c := readFrontDoor(t, h)
	fd := c.Blob.Azure.CDN.FrontDoor
	name, ok := strings.CutPrefix(objectKey, c.Blob.Azure.Container+"/")
	if !ok {
		t.Fatalf("%s is not in the container %s", objectKey, c.Blob.Azure.Container)
	}
	api, err := azure.NewAPI(
		azure.ClientConfig{Account: c.Blob.Azure.Account, Container: c.Blob.Azure.Container},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := api.SignRead(t.Context(), name, rs)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	door, err := cdn.NewFrontDoor(
		cdn.FrontDoorConfig{Endpoint: fd.Endpoint, PathPrefix: fd.PathPrefix},
	)
	if err != nil {
		t.Fatal(err)
	}
	edge, err := door.URL(objectKey, signed.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(edge)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// edgeCapEndsCopies: a URL whose SAS asks the edge to keep its copy for a
// day is fetched while it is valid, so the edge caches it; once the SAS has
// expired and the route's cap, the TTL, has passed too, the edge must refuse
// it. The cap, not the origin's max-age, is what ends the copy (decision
// 0029). Front Door may serve an expired copy while the origin errors, which
// this would catch.
func edgeCapEndsCopies(t *testing.T, h *harness) {
	c := readFrontDoor(t, h)
	key := strings.TrimPrefix(redirectIn(t, h, redirectRepo, artifactPath, nil).Path, "/")
	now := time.Now()
	expiry := now.Add(45 * time.Second)
	u := frontDoorURL(t, h, key, azure.ReadSAS{
		TTL: 45 * time.Second, Start: now, Expiry: expiry, CacheControl: "public, max-age=86400",
	})
	hit := false
	for i := 0; i < 5 && !hit && time.Now().Before(expiry.Add(-5*time.Second)); i++ {
		resp := fetchRange(t, h, u, 0, 1023)
		xc := resp.Header.Get("X-Cache")
		hit = strings.Contains(xc, "HIT")
		t.Logf("while the SAS is valid: X-Cache %q", xc)
	}
	if !hit {
		t.Logf(
			"the edge reported no cache hit while the SAS was valid; the refusal below then shows the origin's check",
		)
	}
	until := expiry.Add(c.Download.Redirect.TTL).Add(time.Minute)
	t.Logf(
		"waiting %s, until the SAS and the %s cap have passed",
		time.Until(until).Round(time.Second),
		c.Download.Redirect.TTL,
	)
	sleepUntil(t, until)
	resp := fetch(t, h, u, http.Header{"Range": {"bytes=0-1023"}})
	drain(t, resp)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		t.Fatalf(
			"the edge served the URL %s after its SAS expired and the cap passed (X-Cache %q, Age %q)",
			redact(u),
			resp.Header.Get("X-Cache"),
			resp.Header.Get("Age"),
		)
	}
	t.Logf("refused with %d %s (X-Cache %q)", resp.StatusCode, resp.Header.Get("X-Ms-Error-Code"),
		resp.Header.Get("X-Cache"))
}

// privateLinkOnly turns the account's public endpoint off, as a deployment
// with no app networks has it: a SAS on the account itself is then refused,
// while the edge still serves a URL it has never cached, over Private Link.
// It runs last, since artifactweave cannot reach the account meanwhile, and
// turns the endpoint back on so the destroy finds what Terraform made.
func privateLinkOnly(t *testing.T, h *harness) {
	c := readFrontDoor(t, h)
	p := "scenario/private-link.bin"
	deployByChecksum(t, h, redirectRepo, p)
	direct := redirectIn(t, h, redirectRepo, p, http.Header{"X-Jfrog-Download-Redirect-To": {"S3"}})
	edge := redirectIn(t, h, redirectRepo, p, nil)
	rg, account := h.output(t, "resource_group"), h.output(t, "account")
	setPublicAccess := func(ctx context.Context, state string) error {
		return exec.CommandContext(ctx, "az", "storage", "account", "update", "--resource-group", rg,
			"--name", account, "--public-network-access", state, "--only-show-errors", "--output", "none").
			Run()
	}
	if err := setPublicAccess(t.Context(), "Disabled"); err != nil {
		t.Fatalf("turn the account's public endpoint off: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), firewallWait)
		defer cancel()
		if err := setPublicAccess(ctx, "Enabled"); err != nil {
			t.Errorf("turn the account's public endpoint back on: %v", err)
		}
	})
	waitForStorage(t, c, false, time.Now().Add(firewallWait))
	resp := fetch(t, h, direct, http.Header{"Range": {"bytes=0-1023"}})
	drain(t, resp)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		t.Fatalf(
			"the account served a SAS directly (%d) with its public endpoint off",
			resp.StatusCode,
		)
	}
	t.Logf("direct request refused with %d %s", resp.StatusCode, resp.Header.Get("X-Ms-Error-Code"))
	fetchRange(t, h, edge, 0, 1023)
}

// sleepUntil waits until when, or fails if the test's context ends first.
func sleepUntil(t *testing.T, when time.Time) {
	t.Helper()
	timer := time.NewTimer(time.Until(when))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("the test ended while waiting")
	}
}
