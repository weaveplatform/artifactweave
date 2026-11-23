package acceptance

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/pkg/containertest"
)

func (w *world) registerSingleReplication(sc *godog.ScenarioContext) {
	sc.Step(`^two fixture-owned replication instances are running on "(sqlite|postgres)"$`, w.startSingleReplication)
	sc.Step(`^the replication instances retain independent repository bytes$`, w.replicationStoresIndependent)
	sc.Step(`^ordinary "(push|pull)" replication transfers scoped bytes, properties and deletes between the instances$`, w.qualifyOrdinaryReplication)
	sc.Step(`^a fresh native Maven consumer resolves the "(push|pull)" replicated package while its source is stopped$`, w.qualifyNativeReplication)
	sc.Step(`^the pinned provider applies, updates, executes and destroys both single replication resources$`, w.qualifyProviderReplication)
	sc.Step(`^ordinary "(push|pull)" replication recovers after process interruption and refuses revoked pending work$`, w.qualifyReplicationRecovery)
	sc.Step(`^an NTLM proxy protects the replication peer$`, w.replicationNTLMProxy)
	sc.Step(`^the "(default|explicit)" NTLM proxy is configured for replication$`, w.configureReplicationNTLM)
	sc.Step(`^the NTLM proxy verified every replication tunnel$`, w.replicationNTLMVerified)
}

func (w *world) startSingleReplication(backend string) error {
	if w.server != nil || w.replicationPeer != nil {
		return fmt.Errorf("replication fixture must own both application lifetimes")
	}
	if backend != "sqlite" && backend != "postgres" {
		return fmt.Errorf("unsupported replication fixture backend")
	}
	if backend == "postgres" && os.Getenv("ARTIFACTWEAVE_TEST_POSTGRES_DSN") == "" {
		// The normal Linux lane supplies Docker, not an ambient database.
		// Preflight explicitly so PostgresDSN cannot silently skip a required
		// direction/backend scenario when short mode or Docker is unavailable.
		if testing.Short() {
			return fmt.Errorf("required PostgreSQL replication qualification cannot run in short mode")
		}
		if err := containertest.CheckLinux(w.t.Context()); err != nil {
			return fmt.Errorf("required PostgreSQL replication qualification needs a test connection or Linux containers: %w", err)
		}
	}
	peer := newWorld(w.t)
	w.replicationPeer = peer
	w.closers = append(w.closers, peer.stop)
	for _, instance := range []*world{w, peer} {
		instance.env = append(instance.env, "ARTIFACTWEAVE_DATABASE_DRIVER="+backend, "ARTIFACTWEAVE_DATABASE_DSN=")
		if backend == "postgres" {
			// Each child receives its own fixture-owned schema. Neither can
			// reset or migrate the other's storage or the connection's default.
			dsn := sqltest.ScratchDSN(w.t, sqltest.PostgresDSN(w.t))
			instance.env = append(instance.env, "ARTIFACTWEAVE_DATABASE_DSN="+dsn)
		}
	}
	if err := peer.start(); err != nil {
		return fmt.Errorf("start owned replication peer: %w", err)
	}
	w.env = append(w.env, "ARTIFACTWEAVE_REMOTE_ALLOWED_PRIVATE_ORIGINS="+peer.url)
	if err := w.start(); err != nil {
		return fmt.Errorf("start owning replication application: %w", err)
	}
	return nil
}

// replicationFixtureRequest addresses only one of the child worlds. Fixture
// administration is separate from the nonadministrator peer transport that the
// transfer scenarios configure; native clients receive isolated credentials.
func replicationFixtureRequest(instance *world, method, path, body string, want int, mediaType ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(instance.t.Context(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, instance.url+path, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("owned replication fixture request: %w", err)
	}
	request.SetBasicAuth("admin", testAdminPassword)
	request.Header.Set("Content-Type", "application/json")
	if len(mediaType) > 0 {
		request.Header.Set("Content-Type", mediaType[0])
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("owned replication fixture transport: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("owned replication fixture response: %w", err)
	}
	if response.StatusCode != want {
		return nil, fmt.Errorf("owned replication fixture %s %s: got%d want%d", method, path, response.StatusCode, want)
	}
	return data, nil
}

func (w *world) replicationStoresIndependent() error {
	if w.replicationPeer == nil {
		return fmt.Errorf("replication peer is not running")
	}
	const key = "p11-replication-isolation"
	for index, instance := range []*world{w, w.replicationPeer} {
		if _, err := replicationFixtureRequest(instance, http.MethodPut, "/artifactory/api/repositories/"+key,
			`{"rclass":"local","packageType":"generic"}`, http.StatusOK); err != nil {
			return err
		}
		if _, err := replicationFixtureRequest(instance, http.MethodPut, "/artifactory/"+key+"/identity.txt",
			fmt.Sprintf("owned-instance-%d", index), http.StatusCreated); err != nil {
			return err
		}
	}
	for index, instance := range []*world{w, w.replicationPeer} {
		actual, err := replicationFixtureRequest(instance, http.MethodGet, "/artifactory/"+key+"/identity.txt", "", http.StatusOK)
		if err != nil {
			return err
		}
		if string(actual) != fmt.Sprintf("owned-instance-%d", index) {
			return fmt.Errorf("replication instances share repository storage")
		}
	}
	return nil
}
