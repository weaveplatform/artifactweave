// Command server models the driver fixture's HTTP API with chi. Only owned
// ephemeral children and spies use it; requests retain method/path only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "spy" {
		spy(os.Args[2:])
		return
	}
	app()
}

func write(path, value string) {
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		panic(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func waitFile(path string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exists(path) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func childAlive(work string) bool {
	data, err := os.ReadFile(filepath.Join(work, "child-pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(string(data))
	return err == nil && syscall.Kill(pid, 0) == nil
}

func recorder(path string) func(http.ResponseWriter, *http.Request, int, string) {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request, status int, body string) {
		mu.Lock()
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		if err := json.NewEncoder(file).Encode(map[string]string{"method": r.Method, "path": r.URL.Path}); err != nil {
			panic(err)
		}
		_ = file.Close()
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func spy(args []string) {
	record, work, port := args[0], args[1], args[2]
	replacement := len(args) > 3
	if replacement {
		if !waitFile(filepath.Join(work, "listener-closed")) {
			return
		}
		data, err := os.ReadFile(filepath.Join(work, "backend-port"))
		if err != nil {
			panic(err)
		}
		port = string(data)
	}
	reply := recorder(filepath.Join(record, "requests.jsonl"))
	router := chi.NewRouter()
	router.Get("/artifactory/api/v1/system/readiness", func(w http.ResponseWriter, r *http.Request) {
		write(filepath.Join(work, "readiness-started"), "")
		if !replacement {
			deadline := time.Now().Add(2 * time.Second)
			for childAlive(work) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
		}
		reply(w, r, http.StatusOK, "ready")
	})
	post := func(w http.ResponseWriter, r *http.Request) {
		if replacement && childAlive(work) {
			write(filepath.Join(work, "replacement-auth-while-child-live"), "")
		}
		reply(w, r, http.StatusOK, `{"access_token":"owned-spy-fixture-token"}`)
	}
	router.Post("/access/api/v1/tokens", post)
	router.Post("/provider-fixture", post)
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		panic(err)
	}
	write(filepath.Join(record, "ready"), strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	if replacement {
		write(filepath.Join(work, "replacement-ready"), "")
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(ln) }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	<-stop
	_ = server.Close()
}

type observedListener struct {
	net.Listener
	closed string
}

func (l observedListener) Close() error {
	err := l.Listener.Close()
	write(l.closed, "")
	return err
}

func app() {
	work := os.Getenv("DRIVER_TEST_ROOT")
	behavior := os.Getenv("DRIVER_TEST_BEHAVIOR")
	write(filepath.Join(work, "child-pid"), strconv.Itoa(os.Getpid()))
	if behavior == "exit" {
		os.Exit(1)
	}
	flags := flag.NewFlagSet("fixture", flag.ExitOnError)
	listen := flags.String("listen", "", "owned listen address")
	config := flags.String("config", "", "private configuration")
	_ = flags.String("base-url", "", "fixture base URL")
	_ = flags.Parse(os.Args[2:])
	if *config != "" {
		data, err := os.ReadFile(*config)
		if err != nil {
			panic(err)
		}
		write(filepath.Join(work, "child-config.json"), string(data))
	}
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "ARTIFACTWEAVE_") || key == "HOME" || key == "GOCOVERDIR" {
			env[key] = value
		}
	}
	data, _ := json.Marshal(env)
	write(filepath.Join(work, "child-env.json"), string(data))
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	if behavior == "no-event" || behavior == "wrong-event" || behavior == "foreign-event" {
		if behavior != "no-event" {
			address := "localhost:1"
			if behavior == "foreign-event" {
				address = "127.0.0.1:" + os.Getenv("DRIVER_TEST_FOREIGN_PORT")
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"msg": "artifactweave listening", "addr": address})
		}
		<-stop
		return
	}
	reply := recorder(filepath.Join(work, "requests.jsonl"))
	release := make(chan struct{})
	router := chi.NewRouter()
	router.Get("/held", func(_ http.ResponseWriter, _ *http.Request) {
		write(filepath.Join(work, "held-request"), "")
		<-release
	})
	router.Get("/artifactory/api/v1/system/readiness", func(w http.ResponseWriter, r *http.Request) {
		if behavior == "exit-on-readiness" {
			write(filepath.Join(work, "readiness-started"), "")
			os.Exit(1)
		}
		status := http.StatusOK
		if behavior == "timeout" {
			status = http.StatusServiceUnavailable
		}
		reply(w, r, status, "ready")
	})
	router.Post("/access/api/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		reply(w, r, http.StatusOK, `{"access_token":"owned-test-token"}`)
	})
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		panic(err)
	}
	write(filepath.Join(work, "backend-port"), strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"msg": "artifactweave listening", "addr": ln.Addr().String()})
	server := &http.Server{Handler: router, ReadHeaderTimeout: time.Second}
	go func() {
		_ = server.Serve(observedListener{Listener: ln, closed: filepath.Join(work, "listener-closed")})
	}()
	<-stop
	if strings.HasPrefix(behavior, "draining") {
		// Match app.Run: Shutdown closes the listener while a held handler
		// keeps the owned process alive. The second signal bounds cleanup.
		go func() { _ = server.Shutdown(context.Background()) }()
		<-stop
	}
	close(release)
	_ = server.Close()
	fmt.Fprintln(os.Stdout, "owned fixture stopped")
}
