package acceptance

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
)

const (
	replicationNTLMKey      = "replication-ntlm"
	replicationNTLMPassword = "Replication-ntlm-fixture-456"
)

// replicationNTLMProxy places the independent NTLMv2 verifier in front of the
// owned peer process. The owner restarts on the same database so its private
// destination allowlist names the proxy as well as the peer.
func (w *world) replicationNTLMProxy() error {
	peer := w.replicationPeer
	if peer == nil {
		return errors.New("NTLM replication requires two owned application processes")
	}
	w.ntlmProxy = fakeupstream.NewNTLMProxy(w.t, fakeupstream.NTLMOptions{
		Username: "proxy-user", Password: replicationNTLMPassword, Domain: "CORP", Workstation: "REPLICATOR", Origins: []string{peer.url},
	})
	w.closers = append(w.closers, w.ntlmProxy.Close)
	return w.restartReplicationProcess("ARTIFACTWEAVE_REMOTE_ALLOWED_PRIVATE_ORIGINS=" + peer.url + "," + w.ntlmProxy.URL)
}

func (w *world) configureReplicationNTLM(selection string) error {
	if w.ntlmProxy == nil {
		return errors.New("NTLM replication proxy is not running")
	}
	address, err := url.Parse(w.ntlmProxy.URL)
	if err != nil {
		return fmt.Errorf("NTLM replication proxy address: %w", err)
	}
	// A valid redirected-host list must not disable ordinary proxy use.
	descriptor := fmt.Sprintf("proxies:\n  %s:\n    host: %q\n    port: %s\n    username: proxy-user\n    password: %s\n"+
		"    domain: CORP\n    ntHost: REPLICATOR\n    redirectedToHosts: %s\n    platformDefault: %t\n",
		replicationNTLMKey, address.Hostname(), address.Port(), replicationNTLMPassword, address.Hostname(), selection == "default")
	if _, err := replicationFixtureRequest(w, http.MethodPatch, "/artifactory/api/system/configuration", descriptor, http.StatusOK, "application/yaml"); err != nil {
		return err
	}
	w.replicationNTLM = selection
	return nil
}

// replicationNTLMVerified requires independent proof of every authenticated
// tunnel and that target credentials stayed inside them.
func (w *world) replicationNTLMVerified() error {
	if w.ntlmProxy == nil {
		return errors.New("NTLM replication proxy is not running")
	}
	authenticated := 0
	for _, record := range w.ntlmProxy.Negotiations() {
		if record.OriginAuthorization {
			return errors.New("replication target credentials reached the NTLM proxy")
		}
		if record.Message == 3 {
			if !record.Authorized || record.Domain != "CORP" || record.Workstation != "REPLICATOR" {
				return fmt.Errorf("NTLM proof/domain/workstation was not verified: %+v", record)
			}
			authenticated++
		}
	}
	if authenticated == 0 {
		return errors.New("replication bypassed its configured NTLM proxy")
	}
	for _, request := range w.ntlmProxy.Requests() {
		if request.Header.Get("Authorization") != "" {
			return errors.New("replication target credentials were sent outside the tunnel")
		}
	}
	return nil
}
