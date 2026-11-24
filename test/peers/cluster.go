package peers

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

// Driver is the metadata database every instance of a cluster opens.
type Driver string

// The two drivers artifactweave serves; a scenario runs on both.
const (
	SQLite   Driver = "sqlite"
	Postgres Driver = "postgres"
)

// AdminUser is the administrator every instance seeds on its first start.
const AdminUser = "admin"

// ProcessLogDirEnv names a directory that keeps each instance's server log
// after the run, as P11's two-process suites use it; CI uploads it when the
// job fails. Without it the logs live in the test's temporary directory.
const ProcessLogDirEnv = "ARTIFACTWEAVE_TEST_PROCESS_LOG_DIR"

const (
	// readyTimeout covers a first start under the race detector: migrations,
	// the production Argon2id hash of the seeded administrator and the
	// production RSA signing key, on three instances at once.
	readyTimeout = 3 * time.Minute
	// stopTimeout is longer than app.Run's two ten-second drains, so an
	// instance that shuts down as serve does is never killed.
	stopTimeout = 30 * time.Second
	// startAttempts bounds the retries of an instance whose reserved port
	// another process took between the reservation and the instance's bind.
	startAttempts = 3
	// logTail bounds each server log a failing scenario prints.
	logTail = 256 << 10
)

// Options says what Start builds.
type Options struct {
	// Instances is how many instances start, at least one; P13's scenarios
	// use two or three.
	Instances int
	// Driver is SQLite (the default) or Postgres.
	Driver Driver
}

// Cluster is a set of artifactweave instances, each in its own OS process
// with its own loopback port, data directory, database, instance identity
// and base URL, and a forwarder for every ordered pair of them.
type Cluster struct {
	// Instances are in start order; Link, Partition and Heal take their
	// indexes.
	Instances []*Instance

	t         *testing.T
	links     map[[2]int]*Link
	client    *http.Client
	postgres  containertest.PostgresEndpoint
	stopPG    func()
	closeOnce sync.Once
}

// Start builds a cluster for t and starts every instance, returning once
// each answers GET /artifactory/api/system/ping. When t ends, the cluster
// stops as Close does and, if t failed, t's output gets each instance's
// server log. With Postgres it skips t where Docker cannot run Linux
// containers.
func Start(t *testing.T, opts Options) *Cluster {
	t.Helper()
	if opts.Instances < 1 {
		t.Fatalf("peers: %d instances; a cluster needs at least one", opts.Instances)
	}
	if opts.Driver == "" {
		opts.Driver = SQLite
	}
	if opts.Driver != SQLite && opts.Driver != Postgres {
		t.Fatalf("peers: unknown driver %q", opts.Driver)
	}
	// TempDir registers its removal now, so it runs after the log dump
	// registered next: cleanups run last-registered first.
	work := t.TempDir()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	c := &Cluster{
		t:      t,
		links:  map[[2]int]*Link{},
		client: &http.Client{Transport: transport, Timeout: time.Minute},
	}
	t.Cleanup(c.dumpLogsOnFailure)
	t.Cleanup(c.Close)
	if opts.Driver == Postgres {
		containertest.RequireLinux(t)
		ep, stop, err := containertest.RunPostgres(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		c.postgres, c.stopPG = ep, stop
	}
	for i := range opts.Instances {
		name := fmt.Sprintf("peer%d", i+1)
		in := &Instance{
			Name:          name,
			DataDir:       filepath.Join(work, name, "data"),
			AdminPassword: "peer-admin-" + strings.ToLower(rand.Text()),
			masterKey:     "peer-master-key-" + strings.ToLower(rand.Text()),
			cluster:       c,
			work:          filepath.Join(work, name),
			done:          make(chan struct{}),
		}
		if err := os.MkdirAll(in.DataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if opts.Driver == Postgres {
			in.dsn = c.createDatabase(t, name)
		}
		c.Instances = append(c.Instances, in)
	}
	// The forwarders listen before any instance starts, because each
	// instance's configuration lists the origins it may reach as a remote;
	// they serve once the instances have their final addresses.
	for i, from := range c.Instances {
		for j, to := range c.Instances {
			if i != j {
				c.links[[2]int{i, j}] = newLink(t, from, to)
			}
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, len(c.Instances))
	for i, in := range c.Instances {
		wg.Go(func() { errs[i] = in.start(c.originsFrom(i)) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	for _, l := range c.links {
		l.serve()
	}
	return c
}

// Link returns the forwarder through which instance from reaches instance
// to. Its URL is the only address from's configuration allows for to.
func (c *Cluster) Link(from, to int) *Link {
	c.t.Helper()
	l, ok := c.links[[2]int{from, to}]
	if !ok {
		c.t.Fatalf("peers: no link from %d to %d", from, to)
	}
	return l
}

// Partition cuts both links between instances a and b; Heal restores them.
func (c *Cluster) Partition(a, b int) {
	c.Link(a, b).Partition()
	c.Link(b, a).Partition()
}

// Heal restores both links between instances a and b.
func (c *Cluster) Heal(a, b int) {
	c.Link(a, b).Heal()
	c.Link(b, a).Heal()
}

// Close stops every instance as serve stops on a signal, closes the
// forwarders and removes the PostgreSQL container. An instance that does not
// stop in time is killed, and an instance that exits unsuccessfully fails t.
// It runs once; Start registers it as a cleanup, and a scenario that checks
// Leftovers calls it first.
func (c *Cluster) Close() {
	c.closeOnce.Do(func() {
		var wg sync.WaitGroup
		for _, in := range c.Instances {
			wg.Go(in.stop)
		}
		wg.Wait()
		for _, l := range c.links {
			l.close()
		}
		c.client.CloseIdleConnections()
		if c.stopPG != nil {
			c.stopPG()
		}
		for _, in := range c.Instances {
			if in.exit != nil {
				c.t.Errorf("peers: %s exited unsuccessfully: %v", in.Name, in.exit)
			}
		}
	})
}

// Leftovers names what a closed cluster still holds: a running instance
// process, an instance or forwarder address that accepts connections, or a
// PostgreSQL container the engine still has. It is empty after Close.
func (c *Cluster) Leftovers(ctx context.Context) []string {
	var left []string
	var addrs []string
	for _, in := range c.Instances {
		select {
		case <-in.done:
		default:
			left = append(left, in.Name+" process is still running")
		}
		if in.addr != "" {
			addrs = append(addrs, in.addr)
		}
	}
	for _, l := range c.links {
		addrs = append(addrs, l.addr)
	}
	if c.postgres.ContainerID != "" {
		if err := containertest.Removed(ctx, c.postgres.ContainerID); err != nil {
			left = append(left, err.Error())
		}
	}
	slices.Sort(addrs)
	for _, addr := range addrs {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			left = append(left, addr+" still accepts connections")
		}
	}
	return left
}

// originsFrom lists the forwarders instance i may reach, the
// remote.allowed_private_origins of its configuration: every other address
// on the loopback interface is refused as an upstream.
func (c *Cluster) originsFrom(i int) []string {
	var origins []string
	for key, l := range c.links {
		if key[0] == i {
			origins = append(origins, l.URL)
		}
	}
	slices.Sort(origins)
	return origins
}

// createDatabase gives one instance a database of its own on the cluster's
// server and returns its DSN. The container's removal drops it.
func (c *Cluster) createDatabase(t *testing.T, name string) string {
	t.Helper()
	admin, err := postgres.OpenDB(c.postgres.DSN, postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("peers: create database %s: %v", name, err)
	}
	u, err := url.Parse(c.postgres.DSN)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func (c *Cluster) dumpLogsOnFailure() {
	if !c.t.Failed() {
		return
	}
	for _, in := range c.Instances {
		if in.logPath == "" {
			continue
		}
		data, err := readTail(in.logPath, logTail)
		if err != nil {
			c.t.Logf("peers: %s server log %s: %v", in.Name, in.logPath, err)
			continue
		}
		c.t.Logf("peers: %s server log (%s):\n%s", in.Name, in.logPath, data)
	}
}

func readTail(path string, limit int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	prefix := ""
	if info.Size() > limit {
		if _, err := f.Seek(info.Size()-limit, io.SeekStart); err != nil {
			return "", err
		}
		prefix = fmt.Sprintf("[first %d bytes omitted]\n", info.Size()-limit)
	}
	data, err := io.ReadAll(f)
	return prefix + string(data), err
}

// logName keeps a log file name to characters every file system accepts.
var logName = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// Instance is one artifactweave process of a cluster.
type Instance struct {
	// Name is peer1, peer2 and so on, in start order.
	Name string
	// URL is the instance's own address, http://127.0.0.1:<port>, which is
	// also its configured base_url. Scenarios address it directly; other
	// instances reach it only through a Link.
	URL string
	// DataDir is the instance's data directory; with SQLite it holds the
	// database too.
	DataDir string
	// AdminPassword is AdminUser's password, different on every instance.
	AdminPassword string

	cluster *Cluster
	work    string
	dsn     string
	// masterKey survives a retried start, which reopens the same database.
	masterKey string
	addr      string
	logPath   string
	stdin     io.WriteCloser
	done      chan struct{}
	exit      error
	kill      func()
}

// Do sends a request to the instance as its administrator and returns the
// status and body; a transport error fails t.
func (in *Instance) Do(t testing.TB, method, path, body string) (int, string) {
	t.Helper()
	status, data, err := in.cluster.request(in.URL, in, method, path, body)
	if err != nil {
		t.Fatalf("%s %s on %s: %v", method, path, in.Name, err)
	}
	return status, data
}

// LogPath is the file that receives the instance's standard output and
// error.
func (in *Instance) LogPath() string { return in.logPath }

func (c *Cluster) request(base string, as *Instance, method, path, body string) (int, string, error) {
	req, err := http.NewRequestWithContext(c.t.Context(), method, base+path, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.SetBasicAuth(AdminUser, as.AdminPassword)
	if strings.HasPrefix(path, "/artifactory/api/") && body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), err
}

// instanceConfig is the strict YAML the instance process loads with
// config.Load, as serve loads --config; only the keys a cluster sets.
type instanceConfig struct {
	Listen    string            `yaml:"listen"`
	BaseURL   string            `yaml:"base_url"`
	DataDir   string            `yaml:"data_dir"`
	MasterKey string            `yaml:"master_key"`
	Database  map[string]string `yaml:"database"`
	Bootstrap map[string]string `yaml:"bootstrap"`
	Log       map[string]string `yaml:"log"`
	Remote    map[string]any    `yaml:"remote"`
}

func (in *Instance) start(origins []string) error {
	var err error
	for range startAttempts {
		if err = in.startOnce(origins); !errors.Is(err, errPortTaken) {
			return err
		}
	}
	return err
}

var errPortTaken = errors.New("reserved port taken")

func (in *Instance) startOnce(origins []string) error {
	addr, err := reservePort()
	if err != nil {
		return err
	}
	in.addr, in.URL = addr, "http://"+addr
	database := map[string]string{"driver": string(SQLite)}
	if in.dsn != "" {
		database = map[string]string{"driver": string(Postgres), "dsn": in.dsn}
	}
	data, err := yaml.Marshal(instanceConfig{
		Listen: addr, BaseURL: in.URL, DataDir: in.DataDir,
		MasterKey: in.masterKey,
		Database:  database,
		Bootstrap: map[string]string{"admin_password": in.AdminPassword},
		Log:       map[string]string{"level": "debug", "format": "text"},
		Remote:    map[string]any{"allowed_private_origins": origins},
	})
	if err != nil {
		return err
	}
	path := filepath.Join(in.work, "artifactweave.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	log, err := in.openLog()
	if err != nil {
		return err
	}
	done := make(chan struct{})
	stdin, kill, err := startProcess(log, path, done, &in.exit)
	if err != nil {
		return fmt.Errorf("%s: %w", in.Name, err)
	}
	in.stdin, in.done, in.kill = stdin, done, kill
	if err := in.awaitReady(); err != nil {
		in.stop()
		if strings.Contains(readLog(in.logPath), "address already in use") {
			in.exit = nil
			return errPortTaken
		}
		return fmt.Errorf("%s: %w; its log is %s", in.Name, err, in.logPath)
	}
	return nil
}

func (in *Instance) openLog() (*os.File, error) {
	dir := os.Getenv(ProcessLogDirEnv)
	if dir == "" {
		dir = in.work
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	pattern := logName.ReplaceAllString(in.cluster.t.Name(), "_") + "-" + in.Name + "-*.log"
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	in.logPath = f.Name()
	return f, nil
}

func readLog(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func (in *Instance) awaitReady() error {
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-in.done:
			return fmt.Errorf("exited before it was ready: %v", in.exit)
		default:
		}
		status, body, err := in.cluster.request(in.URL, in, http.MethodGet, "/artifactory/api/system/ping", "")
		if err == nil && status == http.StatusOK && body == "OK" {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("not ready after %s", readyTimeout)
}

// stop closes the instance's standard input, which ends its serve as a
// signal would, and waits for it to exit.
func (in *Instance) stop() {
	if in.stdin == nil {
		return
	}
	_ = in.stdin.Close()
	select {
	case <-in.done:
	case <-time.After(stopTimeout):
		in.kill()
		<-in.done
	}
}

// reservePort returns a loopback address that was free a moment ago. The
// instance binds it itself, as serve binds its listen address, so another
// process can take it in between; start then retries with another port.
func reservePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	return addr, ln.Close()
}
