package peers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/federation/core/sync/streamtest"
	"github.com/weaveplatform/artifactweave/test/peers"
)

const qualificationStream = "test:p13"

// TestAccStreamInstance adds only a test kind and a private control endpoint.
// The application listener, workers, limits and shutdown are Run's own path.
func TestAccStreamInstance(t *testing.T) {
	peers.RunInstanceWith(t, func(ctx context.Context, cfg config.Config, log *slog.Logger) error {
		rt, err := app.OpenRuntime(ctx, cfg, log, app.RuntimeOptions{})
		if err != nil {
			return err
		}
		kind := &streamtest.Kind{Store: rt.Store}
		if err := rt.Federation.Streams().Kinds.Register(kind); err != nil {
			_ = rt.Close(context.WithoutCancel(ctx))
			return err
		}
		id, err := rt.Store.Instance().Get(ctx)
		if err != nil {
			_ = rt.Close(context.WithoutCancel(ctx))
			return err
		}
		r := chi.NewRouter()
		r.Post("/subscribe", func(w http.ResponseWriter, req *http.Request) {
			var ids []string
			if err := json.NewDecoder(req.Body).Decode(&ids); err != nil {
				http.Error(w, "invalid subscribers", 400)
				return
			}
			kind.Subscribe(qualificationStream, ids...)
			w.WriteHeader(http.StatusNoContent)
		})
		r.Post("/commit", func(w http.ResponseWriter, req *http.Request) {
			var change streamtest.Change
			if err := json.NewDecoder(req.Body).Decode(&change); err != nil {
				http.Error(w, "invalid change", 400)
				return
			}
			e, err := kind.Commit(req.Context(), rt.Federation.Streams(), qualificationStream, change, nil)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_ = json.NewEncoder(w).Encode(e.Seq)
		})
		r.Get("/state", func(w http.ResponseWriter, req *http.Request) {
			state, err := kind.State(req.Context(), qualificationStream, req.URL.Query().Get("origin"))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_ = json.NewEncoder(w).Encode(state)
		})
		r.Get("/outbox", func(w http.ResponseWriter, req *http.Request) {
			entries, err := rt.Store.Outbox().Read(req.Context(), id.InstanceID, qualificationStream, 0, 100)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_ = json.NewEncoder(w).Encode(entries)
		})
		control := httptest.NewServer(r)
		defer control.Close()
		// Discovery only: commands and state use the private loopback API.
		if err := os.WriteFile(filepath.Join(cfg.DataDir, "stream-control-url"), []byte(control.URL), 0o600); err != nil {
			_ = rt.Close(context.WithoutCancel(ctx))
			return err
		}
		return app.RunRuntime(ctx, cfg, rt)
	})
}

func streamControl(t *testing.T, in *peers.Instance, method, path string, input, output any) {
	t.Helper()
	base, err := os.ReadFile(filepath.Join(in.DataDir, "stream-control-url"))
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), method, string(base)+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream control %s: %d %s", path, resp.StatusCode, data)
	}
	if output != nil {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAccPeerStreamSQLite(t *testing.T)   { t.Parallel(); testPeerStream(t, peers.SQLite) }
func TestAccPeerStreamPostgres(t *testing.T) { t.Parallel(); testPeerStream(t, peers.Postgres) }

func testPeerStream(t *testing.T, driver peers.Driver) {
	c := peers.Start(t, peers.Options{Instances: 3, Driver: driver, Retention: retention, InstanceTest: "TestAccStreamInstance"})
	all := c.Instances
	for i, a := range all {
		for _, b := range all[i+1:] {
			pair(t, a, b)
		}
	}
	ids := make([]string, len(all))
	for i, in := range all {
		ids[i] = in.Identity(t).InstanceID
	}
	subscribe := func(i int) {
		var others []string
		for j, id := range ids {
			if i != j {
				others = append(others, id)
			}
		}
		streamControl(t, all[i], http.MethodPost, "/subscribe", others, nil)
	}
	for i := range all {
		subscribe(i)
	}
	commit := func(i int, value string) int64 {
		var seq int64
		streamControl(t, all[i], http.MethodPost, "/commit", streamtest.Change{Type: streamtest.Put, Key: "value", Value: value}, &seq)
		return seq
	}
	state := func(receiver, origin int) streamtest.State {
		var got streamtest.State
		streamControl(t, all[receiver], http.MethodGet, "/state?origin="+ids[origin], nil, &got)
		return got
	}
	awaitHistory := func(receiver, origin, through int) {
		want := make([]string, through)
		for i := range want {
			want[i] = strconv.Itoa(i + 1)
		}
		eventually(t, all[receiver], fmt.Sprintf("origin %d history through %d", origin, through), func() (bool, string) {
			got := state(receiver, origin).Applied
			return slices.Equal(got, want), fmt.Sprint(got)
		})
	}
	for i := range all {
		commit(i, "first")
		for j := range all {
			awaitHistory(j, i, 1)
		}
	}

	// Stop a real receiver process. No fresh commit is made after restart:
	// the production queue/sweep must recover outstanding delivery itself.
	c.Stop(1)
	commit(0, "stopped-2")
	commit(0, "stopped-3")
	c.Restart(1)
	subscribe(1)
	awaitHistory(1, 0, 3)

	c.Partition(0, 1)
	commit(0, "partition-4")
	commit(0, "partition-5")
	c.Heal(0, 1)
	awaitHistory(1, 0, 5)

	// Pull topology is stored exactly as production reads it. The next
	// scheduled sweep, rather than a test call to Engine.Sweep, must pull.
	all[0].SetPeerDelivery(t, ids[1], "pulled")
	all[1].SetPeerDelivery(t, ids[0], "pull")
	commit(0, "pulled-6")
	awaitHistory(1, 0, 6)

	// Exhaust retention while B is disconnected. Inspecting the outbox
	// proves actual pruning instead of assuming a sleep caused it.
	c.Partition(0, 1)
	commit(0, "expired-7")
	deadline := time.Now().Add(retention + 45*time.Second)
	for {
		var entries []struct{ Seq int64 }
		streamControl(t, all[0], http.MethodGet, "/outbox", nil, &entries)
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("production scheduler did not prune: %+v", entries)
		}
		time.Sleep(time.Second)
	}
	before := state(1, 0).Applied
	c.Heal(0, 1)
	eventually(t, all[1], "snapshot after pruning", func() (bool, string) {
		got := state(1, 0)
		return got.Items["value"].Value == "expired-7", fmt.Sprint(got)
	})
	if got := state(1, 0).Applied; !slices.Equal(got, before) {
		t.Fatalf("replayed pruned history: %v, before %v", got, before)
	}
	commit(0, "after-snapshot-8")
	eventually(t, all[1], "stream after snapshot", func() (bool, string) {
		got := state(1, 0)
		want := append(slices.Clone(before), "8")
		return got.Items["value"].Value == "after-snapshot-8" && slices.Equal(got.Applied, want), fmt.Sprint(got)
	})
}
