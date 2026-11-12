package platform

import (
	"bytes"
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

	"github.com/weaveplatform/artifactweave/internal/platform/blob/azure"
	"github.com/weaveplatform/artifactweave/internal/platform/cdn"
)

// azure/cdn (#134): a storage account behind Azure Front Door Premium, which
// reaches it over Private Link. Front Door checks no signature; it passes the
// SAS through to storage. Before artifactweave starts, frontDoorReady
// approves the private endpoint connection Front Door asked for and waits
// for the storage firewall and the edge, until the edge serves a run of new
// URLs. Every request to the edge asks for Front Door's debug headers, so an
// answer a check did not expect says who gave it. The CDN checks of
// cdn_test.go then run with a SAS signer, and two checks follow that only a
// real Front Door can show: the route's cache cap ends an edge copy, and
// Private Link alone reaches the account once its public endpoint is off.

const (
	// frontDoorRollout bounds the wait for Front Door to bring a new
	// endpoint up everywhere and connect it over Private Link, which
	// Microsoft gives as some minutes after the approval.
	frontDoorRollout = 40 * time.Minute
	// firewallWait bounds the wait for the account's network rules to admit
	// or refuse a client after they change.
	firewallWait  = 5 * time.Minute
	frontDoorPoll = 20 * time.Second
	// steadyProbes new URLs, steadyGap apart, must all be served before the
	// checks start: about two minutes of cache misses without a refusal.
	steadyProbes = 12
	steadyGap    = 10 * time.Second
	// frontDoorDebug asks Front Door to add X-Azure-OriginStatusCode,
	// X-Azure-InternalError and X-Azure-ExternalError to its answer
	// (learn.microsoft.com/azure/frontdoor/front-door-http-headers-protocol).
	frontDoorDebug = "X-Azure-DebugInfo"
)

func init() {
	scenarioEdgeHeader["azure/cdn"] = http.Header{http.CanonicalHeaderKey(frontDoorDebug): {"1"}}
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
// address the scenario allows; the edge reaching the account; and the edge
// serving large downloads and then a run of new URLs, both from a readiness
// blob of its own.
func frontDoorReady(t *testing.T, h *harness) {
	t.Helper()
	c := readFrontDoor(t, h)
	deadline := time.Now().Add(frontDoorRollout)
	approvePrivateLink(t, h, deadline)
	waitForStorage(t, c, true, time.Now().Add(firewallWait))
	waitForEdge(t, c, deadline)
	rb := uploadReadinessBlob(t, c)
	waitForLargeDownloads(t, h, c, rb, deadline)
	waitForSteadyEdge(t, h, c, rb, deadline)
	cli(t, "az", append([]string{"storage", "blob", "delete"}, rb.args...)...)
}

// readinessBlob is the blob frontDoorReady fetches through the edge: data,
// stored as name, and the az storage blob arguments that name it.
type readinessBlob struct {
	name string
	data []byte
	args []string
}

// uploadReadinessBlob stores 20 MiB of random data, several of Front Door's
// 8 MiB chunks, with the deployer's login: artifactweave is not running yet.
func uploadReadinessBlob(t *testing.T, c frontDoorConfig) readinessBlob {
	t.Helper()
	data := make([]byte, 20<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	// A content-addressed name under the route's pattern, as artifactweave
	// stores blobs, so the probes take the path real downloads take.
	name := storePrefix(c.Blob.Azure.Prefix) + "sha256/" + digest[:2] + "/" + digest
	file := filepath.Join(t.TempDir(), "readiness.bin")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rb := readinessBlob{name: name, data: data, args: []string{
		"--account-name", c.Blob.Azure.Account, "--container-name", c.Blob.Azure.Container,
		"--name", name, "--auth-mode", "login", "--only-show-errors", "--output", "none",
	}}
	cli(
		t,
		"az",
		append([]string{"storage", "blob", "upload", "--file", file, "--overwrite"}, rb.args...)...)
	return rb
}

// waitForLargeDownloads waits until a download of several of Front Door's
// 8 MiB chunks completes through the edge. The edge answering one request
// is not enough: in runs 38906963602 and 38916559470 the first chunk was
// served and the edge's fetch of the next was reset, minutes after
// waitForEdge returned, while the same download passed in 38925440868 once
// a few more minutes had gone by. Each attempt signs a new URL, so it
// misses the cache and fetches every chunk from the account. Each failed
// attempt is logged: a run has a few, and they show what the edge answers
// while it comes up.
func waitForLargeDownloads(
	t *testing.T,
	h *harness,
	c frontDoorConfig,
	rb readinessBlob,
	deadline time.Time,
) {
	t.Helper()
	sum := sha256.Sum256(rb.data)
	want := hex.EncodeToString(sum[:])
	client := &http.Client{Timeout: 5 * time.Minute}
	started, last := time.Now(), ""
	for {
		now := time.Now()
		u := frontDoorURL(t, h, c.Blob.Azure.Container+"/"+rb.name, azure.ReadSAS{
			TTL: 10 * time.Minute, Start: now, Expiry: now.Add(10 * time.Minute),
			CacheControl: "public, max-age=600",
		})
		got, err := edgeDigest(t, client, u)
		if err == nil && got == want {
			t.Logf("downloads over 8 MiB complete through the edge after a further %s",
				time.Since(started).Round(time.Second))
			return
		}
		last = fmt.Sprintf("%v (digest %.12s)", err, got)
		t.Logf("after a further %s, a download over 8 MiB did not complete: %s",
			time.Since(started).Round(time.Second), last)
		if time.Now().After(deadline) {
			t.Fatalf("a download over 8 MiB did not complete through the edge by %s; last: %s",
				deadline.Format(time.RFC3339), last)
		}
		time.Sleep(frontDoorPoll)
	}
}

// waitForSteadyEdge waits until the edge serves steadyProbes new URLs in a
// row, steadyGap apart. A large download completing once is not enough
// either: in run 42979295008, 24 s after waitForLargeDownloads returned, the
// edge answered 404 to a new URL for a blob it had just served from its
// cache (#1211). Front Door answers 404 to a request no route matches
// (Microsoft's troubleshooting guide), so the likeliest cause is an edge
// server the route had not reached yet. A run of about two minutes of new
// URLs catches such a gap before the checks, whose URLs are new too.
//
// Each probe asks for the start of the readiness blob with its own start
// time and filename in the SAS, so its query, which the edge's cache key
// holds, is new and the edge must fetch from the account, as it does for a
// second name's download. Each probe also opens a connection of its own,
// which may land on another of the edge's servers.
func waitForSteadyEdge(
	t *testing.T,
	h *harness,
	c frontDoorConfig,
	rb readinessBlob,
	deadline time.Time,
) {
	t.Helper()
	want := rb.data[:1024]
	started, streak, last := time.Now(), 0, "none"
	for n := 1; ; n++ {
		now := time.Now()
		u := frontDoorURL(t, h, c.Blob.Azure.Container+"/"+rb.name, azure.ReadSAS{
			TTL: 10 * time.Minute, Start: now, Expiry: now.Add(10 * time.Minute),
			ContentDisposition: probeDisposition(n), CacheControl: "public, max-age=600",
		})
		if why, served := probeEdge(t, u, want); served {
			streak++
			if streak == steadyProbes {
				t.Logf("the edge served %d new URLs in a row after a further %s",
					steadyProbes, time.Since(started).Round(time.Second))
				return
			}
		} else {
			t.Logf("after a further %s, new URL %d ended a run of %d served: %s",
				time.Since(started).Round(time.Second), n, streak, why)
			streak, last = 0, why
		}
		if time.Now().After(deadline) {
			t.Fatalf("the edge had served %d new URLs in a row, not %d, by %s; last refusal: %s",
				streak, steadyProbes, deadline.Format(time.RFC3339), last)
		}
		time.Sleep(steadyGap)
	}
}

// probeDisposition is the response-content-disposition of the readiness
// probe n: a filename of its own, as each item's download has.
func probeDisposition(n int) string {
	return fmt.Sprintf(`attachment; filename="edge-probe-%d.bin"`, n)
}

// probeEdge asks for want, the start of the readiness blob, from u on a
// connection of its own, and says what came back unless it was want.
func probeEdge(t *testing.T, u *url.URL, want []byte) (why string, served bool) {
	t.Helper()
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("http.DefaultTransport is not an *http.Transport")
	}
	transport = transport.Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: time.Minute, Transport: transport}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", len(want)-1))
	req.Header.Set(frontDoorDebug, "1")
	resp, err := client.Do(req)
	if err != nil {
		return withoutURL(err).Error(), false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Sprintf("reading the answer: %v, %s", err, describe(resp, nil)), false
	}
	if strings.Contains(resp.Header.Get("X-Cache"), "HIT") {
		// A URL no one has asked for cannot be in a cache keyed on the
		// whole URL, so its cache key would have lost the query.
		t.Fatalf("the edge answered a new URL from its cache, so its cache key no longer "+
			"holds the query (decision 0029): %s", describe(resp, nil))
	}
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, want) {
		return describe(resp, body), false
	}
	return "", true
}

// edgeDigest downloads u and returns the SHA-256 of what came back, or an
// error that describes an answer other than 200.
func edgeDigest(t *testing.T, client *http.Client, u *url.URL) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(frontDoorDebug, "1")
	resp, err := client.Do(req)
	if err != nil {
		return "", withoutURL(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return "", errors.New(describe(resp, body))
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
// Link is connected. Before that, Front Door answers itself; each kind of
// answer it gives is logged once.
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
	started, last, lastKind := time.Now(), "", ""
	for {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, probe.String(), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(frontDoorDebug, "1")
		resp, err := client.Do(req)
		var kind string
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			if code := resp.Header.Get(
				"X-Ms-Error-Code",
			); code != "" ||
				resp.Header.Get("X-Ms-Request-Id") != "" {
				t.Logf("the edge reaches the account after %s: %s %s",
					time.Since(started).Round(time.Second), resp.Status, code)
				return
			}
			kind, last = resp.Status, "from Front Door itself: "+describe(resp, body)
		} else {
			kind, last = err.Error(), err.Error()
		}
		if kind != lastKind {
			t.Logf("after %s, the edge does not reach the account yet: %s",
				time.Since(started).Round(time.Second), last)
			lastKind = kind
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
			"the edge served the URL %s after its SAS expired and the cap passed: %s",
			redact(u),
			describe(resp, nil),
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
			"the account served a SAS directly (%s) with its public endpoint off",
			describe(resp, nil),
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
