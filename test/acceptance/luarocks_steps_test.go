package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func (w *world) registerLuaRocks(sc *godog.ScenarioContext) {
	sc.Step(`^a LuaRocks project for repository "([^"]*)"$`, w.luaRocksProject)
	sc.Step(`^a LuaRocks upstream serves the packed project$`, w.luaRocksUpstream)
}

func (w *world) luaRocksUpstream() error {
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	rockspec, err := os.ReadFile(filepath.Join(dir, "hello-1.0-1.rockspec"))
	if err != nil {
		return err
	}
	rock, err := os.ReadFile(filepath.Join(dir, "hello-1.0-1.src.rock"))
	if err != nil {
		return err
	}
	manifest := `commands = {}
modules = {}
repository = { ["hello"] = { ["1.0-1"] = { { arch = "rockspec" }, { arch = "src" } } } }
`
	if err = w.remoteUpstream("/rocks/manifest", manifest); err != nil {
		return err
	}
	w.remote.Set("/rocks/manifest-5.4", fakeupstream.Resource{Body: []byte(manifest)})
	w.remote.Set("/rocks/hello-1.0-1.rockspec", fakeupstream.Resource{Body: rockspec})
	w.remote.Set("/rocks/hello-1.0-1.src.rock", fakeupstream.Resource{Body: rock})
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
		filepath.Join(configDir, "config-5.4.lua"),
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
	module := []byte("return { value = 'native LuaRocks' }\n")
	var tarball bytes.Buffer
	gz := gzip.NewWriter(&tarball)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(
		&tar.Header{Name: "hello-1.0/hello.lua", Mode: 0o644, Size: int64(len(module))},
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
		filepath.Join(dir, "hello-1.0.tar.gz"),
		tarball.Bytes(),
		0o600,
	); err != nil {
		return err
	}
	rockspec := `package = "hello"
version = "1.0-1"
source = { url = "file:///work/hello-1.0.tar.gz", dir = "hello-1.0" }
description = { summary = "native acceptance", license = "MIT" }
build = { type = "builtin", modules = { hello = "hello.lua" } }
`
	return os.WriteFile(filepath.Join(dir, "hello-1.0-1.rockspec"), []byte(rockspec), 0o600)
}

func (w *world) runLuaRocks(args string) error {
	return w.runContainer(w.luarocksImage, []string{
		"--entrypoint", "/usr/bin/luarocks-5.4",
		"-e", "LUAROCKS_CONFIG=/work/.luarocks/config-5.4.lua",
	}, w.clientArgs(args))
}
