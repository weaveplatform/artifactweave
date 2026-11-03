package acceptance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

// bowerPrivateStash uses an actual Git repository and an authenticated SSH
// upload-pack service for Bower's client-side pre-registration check. The
// server separately fetches authenticated HTTP refs/provider archives.
func (w *world) bowerPrivateStash() error {
	if err := w.bowerGitUpstream("private", "hello", "1.0.0", "private-stash"); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	gitDir := filepath.Join(w.t.TempDir(), "hello")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		return err
	}
	for _, file := range []struct{ name, body string }{{"bower.json", `{"name":"hello","version":"1.0.0","main":"index.js"}`}, {"index.js", "module.exports = 'private-stash';\n"}} {
		if err := os.WriteFile(
			filepath.Join(gitDir, file.name),
			[]byte(file.body),
			0o600,
		); err != nil {
			return err
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=bower@example.invalid", "-c", "user.name=Bower fixture", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "fixture"}, {"tag", "v1.0.0"}} {
		cmd := exec.CommandContext(w.t.Context(), "git", args...)
		cmd.Dir = gitDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("Bower Git fixture: %w: %s", err, out)
		}
	}
	cmd := exec.CommandContext(
		w.t.Context(),
		"git",
		"upload-pack",
		"--stateless-rpc",
		"--advertise-refs",
		gitDir,
	)
	refs, err := cmd.Output()
	if err != nil {
		return err
	}
	refs = append([]byte("001e# service=git-upload-pack\n0000"), refs...)
	w.remote.Set(
		"/stash/scm/team/hello.git/info/refs",
		fakeupstream.Resource{
			Body:   refs,
			Header: http.Header{"Content-Type": {"application/x-git-upload-pack-advertisement"}},
		},
	)
	cmd = exec.CommandContext(
		w.t.Context(),
		"git",
		"-C",
		gitDir,
		"archive",
		"--format=tar.gz",
		"--prefix=hello-1.0.0/",
		"v1.0.0",
	)
	archive, err := cmd.Output()
	if err != nil {
		return err
	}
	w.remote.Set(
		"/stash/rest/api/1.0/projects/team/repos/hello/archive",
		fakeupstream.Resource{Body: archive},
	)
	cmd = exec.CommandContext(
		w.t.Context(),
		"git",
		"-C",
		gitDir,
		"archive",
		"--format=tar.gz",
		"v1.0.0",
	)
	legacyArchive, err := cmd.Output()
	if err != nil {
		return err
	}
	w.remote.Set(
		"/stash/plugins/servlet/archive/projects/team/repos/hello",
		fakeupstream.Resource{Body: legacyArchive},
	)
	_, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		return err
	}
	privateBlock, err := ssh.MarshalPrivateKey(clientPrivate, "Bower acceptance only")
	if err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(dir, "bower_ssh_key"),
		pem.EncodeToMemory(privateBlock),
		0o600,
	); err != nil {
		return err
	}
	_, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serverSigner, err := ssh.NewSignerFromKey(serverPrivate)
	if err != nil {
		return err
	}
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "git" ||
				!bytes.Equal(key.Marshal(), clientSigner.PublicKey().Marshal()) {
				return nil, fmt.Errorf("Bower fixture SSH key refused")
			}
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(serverSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		return err
	}
	clientHost := "127.0.0.1"
	if os.Getenv("ARTIFACTWEAVE_BOWER_DESKTOP") == "1" {
		clientHost = "host.docker.internal"
	}
	known := "[" + clientHost + "]:" + port + " " + string(
		ssh.MarshalAuthorizedKey(serverSigner.PublicKey()),
	)
	if err := os.WriteFile(
		filepath.Join(dir, "bower_known_hosts"),
		[]byte(known),
		0o600,
	); err != nil {
		listener.Close()
		return err
	}
	passwd := fmt.Sprintf(
		"bower:x:%d:%d:Bower acceptance:/work:/bin/sh\n",
		os.Getuid(),
		os.Getgid(),
	)
	if err := os.WriteFile(filepath.Join(dir, "bower_passwd"), []byte(passwd), 0o644); err != nil {
		_ = listener.Close()
		return err
	}
	sshConfig := "Host 127.0.0.1\n  Hostname " + clientHost + "\n  IdentityFile /work/bower_ssh_key\n  UserKnownHostsFile /work/bower_known_hosts\n  StrictHostKeyChecking yes\n  IdentitiesOnly yes\n  BatchMode yes\n"
	if err := os.WriteFile(
		filepath.Join(dir, "bower_ssh_config"),
		[]byte(sshConfig),
		0o600,
	); err != nil {
		listener.Close()
		return err
	}
	ctx, cancel := context.WithCancel(w.t.Context())
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() { serveBowerSSH(ctx, conn, config, gitDir) })
		}
	})
	w.t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	w.vars["bowerSSH"] = "ssh://git@127.0.0.1:" + port + "/team/hello.git"
	return nil
}

func serveBowerSSH(ctx context.Context, conn net.Conn, config *ssh.ServerConfig, gitDir string) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for request := range channels {
		if request.ChannelType() != "session" {
			_ = request.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := request.Accept()
		if err != nil {
			return
		}
		for request := range requests {
			var payload struct{ Command string }
			if request.Type != "exec" || ssh.Unmarshal(request.Payload, &payload) != nil ||
				payload.Command != "git-upload-pack '/team/hello.git'" {
				if request.WantReply {
					_ = request.Reply(false, nil)
				}
				continue
			}
			if request.WantReply {
				_ = request.Reply(true, nil)
			}
			cmd := exec.CommandContext(ctx, "git", "upload-pack", gitDir)
			cmd.Stdin = channel
			cmd.Stdout = channel
			cmd.Stderr = channel.Stderr()
			status := uint32(0)
			if cmd.Run() != nil {
				status = 1
			}
			_, _ = channel.SendRequest(
				"exit-status",
				false,
				ssh.Marshal(struct{ Status uint32 }{status}),
			)
			_ = channel.Close()
			break
		}
	}
}

func (w *world) bowerExternalFixture() error {
	if err := w.bowerUpstream("dep", "2.0.0", "external-dep", "unused"); err != nil {
		return err
	}
	dep, err := bowerArchive("dep", "2.0.0", "external-dep")
	if err != nil {
		return err
	}
	w.remote.Set("/external/dep.tar.gz", fakeupstream.Resource{Body: dep})
	w.remote.Set("/git/team/dep/archive/refs/tags/v2.0.0.tar.gz", fakeupstream.Resource{Body: dep})
	w.remote.Set("/git/team/dep/archive/refs/heads/master.tar.gz", fakeupstream.Resource{Body: dep})
	ref := strings.Repeat("a", 40) + " refs/tags/v2.0.0\n"
	w.remote.Set(
		"/git/team/dep.git/info/refs",
		fakeupstream.Resource{Body: []byte(fmt.Sprintf("%04x%s0000", len(ref)+4, ref))},
	)
	gitdep, err := bowerArchive("gitdep", "2.0.0", "exact-git-dep")
	if err != nil {
		return err
	}
	w.remote.Set(
		"/git/team/gitdep/archive/refs/tags/v2.0.0.tar.gz",
		fakeupstream.Resource{Body: gitdep},
	)
	w.remote.Set(
		"/git/team/gitdep.git/info/refs",
		fakeupstream.Resource{Body: []byte(fmt.Sprintf("%04x%s0000", len(ref)+4, ref))},
	)
	if err := w.bowerPackage(
		"native",
		"2.0.0",
		"native-local-dep",
		"external-fixture",
	); err != nil {
		return err
	}
	if err := w.bowerPackage("hello", "1.0.0", "external-parent", "external-fixture"); err != nil {
		return err
	}
	dir, err := w.filesDir()
	if err != nil {
		return err
	}
	manifest := fmt.Sprintf(
		`{"name":"hello","version":"1.0.0","dependencies":{"dep":%s,"gitdep":%s,"native":"art://bower-rewrite/native#2.0.0"}}`,
		strconv.Quote(w.remote.URL+"/external/dep.tar.gz"),
		strconv.Quote(w.remote.URL+"/git/team/gitdep.git#2.0.0"),
	)
	return os.WriteFile(
		filepath.Join(dir, "external-fixture", "hello", "1.0.0", "bower.json"),
		[]byte(manifest),
		0o600,
	)
}
