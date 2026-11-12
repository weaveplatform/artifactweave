package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

func (w *world) registerLuaRocks(sc *godog.ScenarioContext) {
	sc.Step(`^a LuaRocks project for repository "([^"]*)"$`, w.luaRocksProject)
	sc.Step(`^a LuaRocks upstream serves the packed project$`, w.luaRocksUpstream)
	sc.Step(
		`^a LuaRocks virtual project with a remote dependency for repository "([^"]*)"$`,
		w.luaRocksVirtualProject,
	)
	sc.Step(`^a LuaRocks upstream serves the packed dependency$`, w.luaRocksDependencyUpstream)
	sc.Step(
		`^the server answered no request with 500 since the last luarocks command$`,
		w.luaRocksAnsweredNo500,
	)
}

func (w *world) luaRocksUpstream() error {
	return w.luaRocksUpstreamPackage("hello")
}

func (w *world) luaRocksDependencyUpstream() error {
	return w.luaRocksUpstreamPackage("remoteonly")
}

func (w *world) luaRocksUpstreamPackage(name string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	rockspec, err := os.ReadFile(filepath.Join(dir, name+"-1.0-1.rockspec"))
	if err != nil {
		return err
	}
	rock, err := os.ReadFile(filepath.Join(dir, name+"-1.0-1.src.rock"))
	if err != nil {
		return err
	}
	manifest := fmt.Sprintf(`commands = {}
modules = {}

repository = { [%q] = { ["1.0-1"] = { { arch = "rockspec" }, { arch = "src" } } } }
`, name)
	if err = w.remoteUpstream("/rocks/manifest", manifest); err != nil {
		return err
	}
	w.remote.Set("/rocks/manifest-5.4", fakeupstream.Resource{Body: []byte(manifest)})
	w.remote.Set("/rocks/manifest-5.5", fakeupstream.Resource{Body: []byte(manifest)})
	w.remote.Set("/rocks/"+name+"-1.0-1.rockspec", fakeupstream.Resource{Body: rockspec})
	w.remote.Set("/rocks/"+name+"-1.0-1.src.rock", fakeupstream.Resource{Body: rock})
	return nil
}

func (w *world) luaRocksProject(repoKey string) error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	configDir := filepath.Join(dir, ".luarocks")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	server := "http://admin:" + testAdminPassword + "@" + strings.TrimPrefix(
		w.url,
		"http://",
	) + "/artifactory/api/luarocks/" + repoKey
	config := fmt.Sprintf(
		"rocks_servers = {%q}\nfs_use_modules = false\nvariables = { CURL = 'curl' }\n",
		server,
	)
	if err := os.WriteFile(
		filepath.Join(configDir, "config-5.5.lua"),
		[]byte(config),
		0o600,
	); err != nil {
		return err
	}
	upload := fmt.Sprintf("key = %q\nserver = %q\n", "acceptance-key", server)
	if err := os.WriteFile(
		filepath.Join(configDir, "upload_config.lua"),
		[]byte(upload),
		0o600,
	); err != nil {
		return err
	}
	return writeLuaRocksPackage(dir, "hello")
}

func (w *world) luaRocksVirtualProject(repoKey string) error {
	if err := w.luaRocksProject(repoKey); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	for _, name := range []string{"localonly", "remoteonly"} {
		if err := writeLuaRocksPackage(dir, name); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, "hello-1.0-1.rockspec")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = []byte(strings.Replace(string(data), "build =", `dependencies = {"remoteonly >= 1.0"}
build =`, 1))
	return os.WriteFile(path, data, 0o600)
}

func writeLuaRocksPackage(dir, name string) error {
	module := []byte("return { value = 'native LuaRocks' }\n")
	var tarball bytes.Buffer
	gz := gzip.NewWriter(&tarball)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(
		&tar.Header{Name: name + "-1.0/" + name + ".lua", Mode: 0o644, Size: int64(len(module))},
	); err != nil {
		return err
	}
	if _, err := tw.Write(module); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(dir, name+"-1.0.tar.gz"),
		tarball.Bytes(),
		0o600,
	); err != nil {
		return err
	}
	rockspec := fmt.Sprintf(`package = %q
version = "1.0-1"
source = { url = "file:///work/%s-1.0.tar.gz", dir = "%s-1.0" }
description = { summary = "native acceptance", license = "MIT" }
build = { type = "builtin", modules = { [%q] = %q } }
`, name, name, name, name, name+".lua")
	return os.WriteFile(filepath.Join(dir, name+"-1.0-1.rockspec"), []byte(rockspec), 0o600)
}

func (w *world) runLuaRocks(args string) error {
	w.luaRocksLog = len(w.stderr.String())
	err := w.runContainer(w.luarocksImage, []string{
		"--entrypoint", "/usr/local/bin/luarocks",
		"-e", "LUAROCKS_CONFIG=/work/.luarocks/config-5.5.lua",
	}, w.clientArgs(args))
	if err != nil {
		return err
	}
	if w.lastRC == 0 {
		return nil
	}
	var requests []string
	for _, request := range w.requests("GET") {
		if strings.Contains(request, "/api/luarocks/") {
			requests = append(requests, request)
		}
	}
	return fmt.Errorf("LuaRocks exited %d; GET responses: %v; output: %s",
		w.lastRC, requests, w.body)
}

// luaRocksAnsweredNo500 fails when the server answered 500 to a request made
// since the last luarocks command started. The client asks for
// manifest-5.5.zip before manifest-5.5 and moves on to the next name when one
// fails (luarocks 3.13.0 manif.lua), so an install succeeds over a 500: before
// #1261's fix, manifest-5.5.zip answered 500 and the scenario passed (#1270).
func (w *world) luaRocksAnsweredNo500() error {
	if err := w.awaitLoggedSoFar(); err != nil {
		return err
	}
	var answered, logged []string
	for line := range strings.Lines(w.stderr.String()[w.luaRocksLog:]) {
		var rec struct {
			Msg, Level, Method, Path string
			Status                   int
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		switch {
		case rec.Msg == "request" && rec.Status == http.StatusInternalServerError:
			answered = append(answered, rec.Method+" "+rec.Path)
		case rec.Msg == "panic":
			// RecoverPanic sits outside AccessLog, so a request whose handler
			// panicked is answered 500 with this line and no request line.
			answered = append(answered, "panic at "+rec.Path)
		}
		if rec.Level == "ERROR" || rec.Level == "WARN" {
			logged = append(logged, strings.TrimSpace(line))
		}
	}
	if len(answered) == 0 {
		return nil
	}
	return fmt.Errorf(
		"the server answered 500 to %s since the last luarocks command; server log:\n%s",
		strings.Join(answered, ", "),
		strings.Join(logged, "\n"),
	)
}

// awaitLoggedSoFar returns once every request answered so far is in the
// server's log. An access line reaches w.stderr through exec's copying
// goroutine after the response it records, so the client's last request can
// trail the client's exit. A ping sent now is answered after the client's
// requests, so once the ping's line is in the log, theirs are too.
func (w *world) awaitLoggedSoFar() error {
	id := "after-luarocks-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		w.url+"/artifactory/api/system/ping",
		nil,
	)
	if err != nil {
		return err
	}
	req.Header.Set("X-Request-ID", id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(w.stderr.String(), id); {
		if time.Now().After(deadline) {
			return fmt.Errorf("the server's log has no line for request %q", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}
