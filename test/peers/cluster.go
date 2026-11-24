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

	"github.com/weaveplatform/artifactweave/internal/app"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
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

// EgressProxy is the key of the proxy configuration that makes each
// instance's egress its platform default proxy.
const EgressProxy = "peers-egress"

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
// with its own loopback port, data directory, database, instance identity,
// ingress address and egress proxy.
type Cluster struct {
	// Instances are in start order; Cut, Partition, DoFrom and their kin
	// take their indexes.
	Instances []*Instance

	t *testing.T
	// ingresses maps each instance's ingress address to its index: the
	// only destinations the egress proxies carry connections to.
	ingresses map[string]int
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
		t:         t,
		ingresses: map[string]int{},
		client:    &http.Client{Transport: transport, Timeout: time.Minute},
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
			// Both relays listen before any instance starts: the ingress
			// is the base URL the instance's configuration names, and the
			// egress and the other ingresses are the origins it may reach.
			// They serve once the instances have their final addresses.
			ingress: listenRelay(t),
			egress:  listenRelay(t),
		}
		in.URL = "http://" + in.ingress.addr
		c.ingresses[in.ingress.addr] = i
		proxied := transport.Clone()
		proxied.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: in.egress.addr})
		// A fresh connection per request, as the egress serves a plain
		// HTTP request, so a cut never meets a pooled connection.
		proxied.DisableKeepAlives = true
		in.via = &http.Client{Transport: proxied, Timeout: time.Minute}
		if err := os.MkdirAll(in.DataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if opts.Driver == Postgres {
			in.dsn = c.createDatabase(t, name)
		}
		c.Instances = append(c.Instances, in)
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
	for _, in := range c.Instances {
		in.ingress.serveIngress(in.addr)
		in.egress.serveEgress(func(addr string) (int, bool) {
			i, ok := c.ingresses[addr]
			return i, ok
		})
	}
	for _, in := range c.Instances {
		in.proxyThroughEgress(t)
	}
	return c
}

// Cut stops instance from's server reaching instance to: its egress resets
// every connection it carries to to's ingress and refuses new ones until
// Restore. The cut is by source, so nothing else reaches to any less: the
// other instances, the scenario, and to's own calls to from all still pass.
func (c *Cluster) Cut(from, to int) {
	c.t.Helper()
	c.instance(from).egress.setCut(c.index(to), true)
}

// Restore lets instance from's server reach instance to again.
func (c *Cluster) Restore(from, to int) {
	c.t.Helper()
	c.instance(from).egress.setCut(c.index(to), false)
}

// Partition cuts instances a and b from each other, both ways; every other
// pair still reaches each other.
func (c *Cluster) Partition(a, b int) {
	c.t.Helper()
	c.Cut(a, b)
	c.Cut(b, a)
}

// Heal restores both ways between instances a and b.
func (c *Cluster) Heal(a, b int) {
	c.t.Helper()
	c.Restore(a, b)
	c.Restore(b, a)
}

// DoFrom sends a request to instance to's base URL as to's administrator,
// the way from's server reaches it: through from's egress. It returns a
// transport error, not a failed t, so a scenario can see a cut.
func (c *Cluster) DoFrom(from, to int, method, path, body string) (int, string, error) {
	c.t.Helper()
	target := c.instance(to)
	return c.send(c.instance(from).via, target.URL, AdminUser, target.AdminPassword, "", method, path, body)
}

func (c *Cluster) instance(i int) *Instance {
	c.t.Helper()
	return c.Instances[c.index(i)]
}

func (c *Cluster) index(i int) int {
	c.t.Helper()
	if i < 0 || i >= len(c.Instances) {
		c.t.Fatalf("peers: no instance %d in a cluster of %d", i, len(c.Instances))
	}
	return i
}

// Close stops every instance as serve stops on a signal, closes the
// ingresses and egresses and removes the PostgreSQL container. An instance that does not
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
		for _, in := range c.Instances {
			in.ingress.close()
			in.egress.close()
			in.via.CloseIdleConnections()
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
// process, an instance, ingress or egress address that accepts connections, or a
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
		addrs = append(addrs, in.ingress.addr, in.egress.addr)
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

// originsFrom lists what instance i's server may reach on the loopback
// interface, the remote.allowed_private_origins of its configuration: the
// other instances' base URLs, and its own egress, which the peer client
// checks as it checks a destination. The egress carries connections only
// to the ingresses, so with it as the platform default proxy an instance
// reaches no other at its listening address, around a cut.
func (c *Cluster) originsFrom(i int) []string {
	origins := []string{"http://" + c.Instances[i].egress.addr}
	for j, in := range c.Instances {
		if j != i {
			origins = append(origins, in.URL)
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
	// URL is the instance's ingress, http://127.0.0.1:<port>, a forwarder to
	// the port the instance listens on, and its configured base_url: the
	// one address every other instance and the scenario reach it at, and so
	// the one that pairing records for it.
	URL string
	// DataDir is the instance's data directory; with SQLite it holds the
	// database too.
	DataDir string
	// AdminPassword is AdminUser's password, different on every instance.
	AdminPassword string

	cluster *Cluster
	work    string
	dsn     string
	// config is the configuration file the instance process loads.
	config string
	// ingress forwards URL to addr; egress is the instance's platform
	// default proxy, which Cut refuses destinations at; via is a client
	// that sends through egress, as the instance's own server does.
	ingress, egress *relay
	via             *http.Client
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
	return in.DoAs(t, AdminUser, in.AdminPassword, method, path, body)
}

// DoAs is Do as user with password, or anonymously when user is empty.
func (in *Instance) DoAs(t testing.TB, user, password, method, path, body string) (int, string) {
	t.Helper()
	status, data, err := in.cluster.send(in.cluster.client, in.URL, user, password, "", method, path, body)
	if err != nil {
		t.Fatalf("%s %s on %s: %v", method, path, in.Name, err)
	}
	return status, data
}

// Identity reads the instance's identity, its private key included, from
// its database, as its own server does: a scenario can then require that no
// answer disclosed the key. The instance must be running; a PostgreSQL
// cluster's database goes with its container.
func (in *Instance) Identity(t testing.TB) instance.InstanceIdentity {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{File: in.config})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := app.Keyring(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := app.OpenStore(t.Context(), cfg, appsql.Options{SkipMigrate: true, Keyring: keys})
	if err != nil {
		t.Fatalf("peers: open %s's store: %v", in.Name, err)
	}
	defer func() { _ = st.Close() }()
	id, err := st.Instance().Get(t.Context())
	if err != nil {
		t.Fatalf("peers: read %s's identity: %v", in.Name, err)
	}
	return id
}

// LogPath is the file that receives the instance's standard output and
// error.
func (in *Instance) LogPath() string { return in.logPath }

// DoHeader is Do with request headers, such as a manifest's Content-Type
// or a registry client's Accept, returning the answer's headers too.
func (in *Instance) DoHeader(t testing.TB, method, path, body string, header http.Header) (int, http.Header, string) {
	t.Helper()
	status, got, data, err := in.cluster.sendHeader(in.cluster.client, in.URL, AdminUser, in.AdminPassword, header, method, path, body)
	if err != nil {
		t.Fatalf("%s %s on %s: %v", method, path, in.Name, err)
	}
	return status, got, data
}

// send sends a request through client to base, as user unless user is
// empty. A body without contentType is JSON under the REST APIs' prefixes
// and has no type elsewhere, where it is an artifact's content.
func (c *Cluster) send(client *http.Client, base, user, password, contentType, method, path, body string) (int, string, error) {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	status, _, data, err := c.sendHeader(client, base, user, password, header, method, path, body)
	return status, data, err
}

func (c *Cluster) sendHeader(
	client *http.Client, base, user, password string, header http.Header, method, path, body string,
) (int, http.Header, string, error) {
	req, err := http.NewRequestWithContext(c.t.Context(), method, base+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, "", err
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if req.Header.Get("Content-Type") == "" && body != "" &&
		(strings.HasPrefix(path, "/artifactory/api/") || strings.HasPrefix(path, "/access/api/")) {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(data), err
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
	in.addr = addr
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
	in.config = path
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

// proxyThroughEgress makes the instance's egress its platform default
// proxy, which its remote, push and peer clients all take. Cut relies on
// it: an instance that reached the others directly would cross every cut.
// A scenario that adds a proxy must not make it a platform default too, nor
// set a remote's disableProxy, or that traffic would bypass the egress.
func (in *Instance) proxyThroughEgress(t *testing.T) {
	t.Helper()
	host, port, err := net.SplitHostPort(in.egress.addr)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := fmt.Sprintf("proxies:\n  %s:\n    host: %s\n    port: %s\n    platformDefault: true\n", EgressProxy, host, port)
	status, body, err := in.cluster.send(in.cluster.client, in.URL, AdminUser, in.AdminPassword, "application/yaml",
		http.MethodPatch, "/artifactory/api/system/configuration", descriptor)
	if err != nil || status != http.StatusOK {
		t.Fatalf("peers: make %s's egress its platform default proxy: %d %s (%v)", in.Name, status, body, err)
	}
}

func (in *Instance) awaitReady() error {
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-in.done:
			return fmt.Errorf("exited before it was ready: %v", in.exit)
		default:
		}
		// The instance's own address: its ingress serves only once every
		// instance is ready.
		status, body, err := in.cluster.send(in.cluster.client, "http://"+in.addr, AdminUser, in.AdminPassword, "",
			http.MethodGet, "/artifactory/api/system/ping", "")
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
