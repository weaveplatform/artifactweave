package federation_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	pushProcessConfig = "ARTIFACTWEAVE_TEST_PUSH_PROCESS_CONFIG"
	pushProcessReady  = "ARTIFACTWEAVE_PUSH_TARGET_READY="
)

type pushProcessOptions struct {
	DataDir   string `yaml:"data_dir"`
	Driver    string `yaml:"driver"`
	DSN       string `yaml:"dsn"`
	MasterKey string `yaml:"master_key"`
}

// TestAccSinglePushTargetProcess is entered only by startPushTarget. It runs
// the production runtime/router in a different OS process, reading its owned
// private configuration file and shutting down when its owner closes stdin.
func TestAccSinglePushTargetProcess(t *testing.T) {
	t.Parallel()
	path := os.Getenv(pushProcessConfig)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read private target configuration")
	}
	var options pushProcessOptions
	if yaml.Unmarshal(data, &options) != nil {
		t.Fatal("decode private target configuration")
	}
	// Recreate production defaults rather than serializing internal config
	// types whose custom YAML readers accept only operator-facing forms.
	cfg := config.Default()
	cfg.DataDir, cfg.MasterKey = options.DataDir, options.MasterKey
	cfg.Database.Driver, cfg.Database.DSN = options.Driver, options.DSN
	cfg.Bootstrap.AdminPassword = "pw"
	runtime, err := app.OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), harness.AppOptions())
	if err != nil {
		t.Fatal("open target runtime", err) // Output is captured in the owner's private subprocess log.
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.Background()); err != nil {
			t.Error("close target runtime")
		}
	})
	for _, receiver := range []struct {
		name    string
		actions accessapi.Actions
	}{
		{"receiver", accessapi.ActionRead | accessapi.ActionWrite | accessapi.ActionDelete | accessapi.ActionAnnotate},
		{"receiver-readonly", accessapi.ActionRead},
	} {
		if err := runtime.Store.Users().Create(t.Context(), accesstest.User{Name: receiver.name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal("create native target receiver", err)
		}
		if err := runtime.Store.Permissions().
			Create(t.Context(), accesstest.Permission{Name: receiver.name + "-target-only", Resources: map[accessapi.ResourceType]accesstest.Resource{
				accessapi.ResourceArtifact: {
					Targets: map[string]accesstest.Target{"target": {}},
					Actions: accesstest.Grants{Users: map[string]accessapi.Actions{receiver.name: receiver.actions}},
				},
			}}); err != nil {
			t.Fatal("grant native receiver authority", err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen on owned target loopback")
	}
	server := &http.Server{Handler: pushRuntimeRouter(runtime), ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	if _, err := fmt.Fprintln(os.Stdout, pushProcessReady+"http://"+listener.Addr().String()); err != nil {
		t.Fatal("publish owned target address")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Error("drain target server")
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Error("target server terminated unexpectedly")
	}
}

func pushRuntimeRouter(runtime *app.Runtime) http.Handler {
	return harness.Router(harness.Config{
		Store: runtime.Store, Blobs: runtime.Blobs, Auth: runtime.Auth, Tokens: runtime.Tokens, Keys: runtime.Keys,
		Federation: runtime.Federation, Work: runtime.Work,
		PasswordHash:  support.TestHash,
		RemoteSources: runtime.Artifacts.RemoteSources(), RegistrySources: runtime.Artifacts.RegistrySources(),
		RemoteHTTP: runtime.Artifacts.RemoteHTTP(), VirtualCache: runtime.Artifacts.VirtualCache(),
		Logger: slog.New(slog.DiscardHandler),
	})
}

func startPushTarget(t *testing.T, cfg config.Config) string {
	t.Helper()
	data, err := yaml.Marshal(pushProcessOptions{DataDir: cfg.DataDir, Driver: cfg.Database.Driver, DSN: cfg.Database.DSN, MasterKey: cfg.MasterKey})
	if err != nil {
		t.Fatal("encode private target configuration")
	}
	path := filepath.Join(t.TempDir(), "target.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	command := exec.CommandContext(ctx, executable, "-test.run=^TestAccSinglePushTargetProcess$")
	command.WaitDelay = 10 * time.Second
	command.Env = append(os.Environ(), pushProcessConfig+"="+path)
	logDir := os.Getenv("ARTIFACTWEAVE_TEST_PROCESS_LOG_DIR")
	if logDir == "" {
		logDir = t.TempDir()
	}
	log, err := os.CreateTemp(logDir, "push-target-*.log")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	command.Stderr = log
	output, err := command.StdoutPipe()
	if err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	input, err := command.StdinPipe()
	if err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatal("start target process")
	}
	ready := make(chan string, 1)
	done := make(chan struct{})
	var exit error
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(log, line)
			if address, ok := strings.CutPrefix(line, pushProcessReady); ok {
				ready <- address
			}
		}
		exit = command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = input.Close()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			cancel()
			<-done
		}
		cancel()
		_ = log.Close()
		if exit != nil {
			t.Error("target process exited unsuccessfully; inspect its private fixture log")
		}
	})
	select {
	case address := <-ready:
		return address
	case <-done:
		t.Fatal("target process did not become ready; inspect its private fixture log")
	case <-time.After(45 * time.Second):
		cancel()
		t.Fatal("target process startup timed out")
	}
	return ""
}
