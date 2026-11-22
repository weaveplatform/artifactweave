package acceptance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const replicationTransportPassword = "Replication-fixture-only-123"

func (w *world) qualifyOrdinaryReplication(direction string) (result error) {
	defer func() { result = errors.Join(result, w.replicationScenarioEvidence(direction, "generic", nil)) }()
	fixture, err := w.configureReplicationPair(direction, "generic")
	if err != nil {
		return err
	}
	upstream, consumer := fixture.upstream, fixture.consumer
	owningRepo, sourceKey, consumerKey := fixture.owningRepo, fixture.sourceKey, fixture.consumerKey
	put := func(instance *world, path, body string, status int) error {
		_, err := replicationFixtureRequest(instance, http.MethodPut, path, body, status)
		return err
	}

	for _, path := range []string{"selected/current.bin", "selected/gone.bin", "selected/omitted/hidden.bin", "private/untouched.bin"} {
		if err := put(upstream, "/artifactory/"+sourceKey+"/"+path, "initial replicated bytes", http.StatusCreated); err != nil {
			return err
		}
	}
	control := "unrelated destination bytes"
	if direction == "push" {
		if err := put(consumer, "/artifactory/"+consumerKey+"/private/untouched.bin", control, http.StatusCreated); err != nil {
			return err
		}
	} else {
		control = "initial replicated bytes"
		// Warm an unrelated owning-cache entry, then remove only its upstream
		// file. Selected deletion must preserve the cached control outside scope.
		if err := replicationBytes(w, owningRepo, "private/untouched.bin", control); err != nil {
			return err
		}
		if _, err := replicationFixtureRequest(upstream, http.MethodDelete, "/artifactory/"+sourceKey+"/private/untouched.bin", "", http.StatusNoContent); err != nil {
			return err
		}
	}
	if err := put(upstream, "/artifactory/api/storage/"+sourceKey+"/selected/current.bin?properties=qualification=initial", "", http.StatusNoContent); err != nil {
		return err
	}
	if err := w.executeAndWaitReplication(owningRepo); err != nil {
		return err
	}
	for _, path := range []string{"selected/current.bin", "selected/gone.bin"} {
		if err := replicationBytes(consumer, consumerKey, path, "initial replicated bytes"); err != nil {
			return err
		}
	}
	for _, path := range []string{"selected/omitted/hidden.bin"} {
		if _, err := replicationFixtureRequest(consumer, http.MethodGet, "/artifactory/"+consumerKey+"/"+path, "", http.StatusNotFound); err != nil {
			return err
		}
	}
	if err := put(upstream, "/artifactory/"+sourceKey+"/selected/current.bin", "updated replicated bytes", http.StatusCreated); err != nil {
		return err
	}
	if err := put(upstream, "/artifactory/api/storage/"+sourceKey+"/selected/current.bin?properties=qualification=updated", "", http.StatusNoContent); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(upstream, http.MethodDelete, "/artifactory/"+sourceKey+"/selected/gone.bin", "", http.StatusNoContent); err != nil {
		return err
	}
	if err := w.executeAndWaitReplication(owningRepo); err != nil {
		return err
	}
	if err := replicationBytes(consumer, consumerKey, "selected/current.bin", "updated replicated bytes"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(consumer, http.MethodGet, "/artifactory/"+consumerKey+"/selected/gone.bin", "", http.StatusNotFound); err != nil {
		return err
	}
	if err := replicationBytes(consumer, consumerKey, "private/untouched.bin", control); err != nil {
		return err
	}
	if err := replicationQualificationProperty(consumer, consumerKey, "updated"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replications/"+owningRepo,
		`{"syncProperties":false,"syncDeletes":false}`, http.StatusOK); err != nil {
		return err
	}
	// Keep the digest unchanged: replacing bytes has its own clearOldFileData
	// baseline. This control isolates property synchronization from replacement.
	if err := put(upstream, "/artifactory/api/storage/"+sourceKey+"/selected/current.bin?properties=qualification=must-not-apply", "", http.StatusNoContent); err != nil {
		return err
	}
	if err := w.executeAndWaitReplication(owningRepo); err != nil {
		return err
	}
	if err := replicationBytes(consumer, consumerKey, "selected/current.bin", "updated replicated bytes"); err != nil {
		return err
	}
	if err := replicationQualificationProperty(consumer, consumerKey, "updated"); err != nil {
		return err
	}
	if _, err := replicationFixtureRequest(upstream, http.MethodDelete, "/artifactory/"+sourceKey+"/selected/current.bin", "", http.StatusNoContent); err != nil {
		return err
	}
	if err := w.executeAndWaitReplication(owningRepo); err != nil {
		return err
	}
	if err := replicationBytes(consumer, consumerKey, "selected/current.bin", "updated replicated bytes"); err != nil {
		return err
	}
	return replicationBytes(consumer, consumerKey, "private/untouched.bin", control)
}

func replicationQualificationProperty(instance *world, repository, expected string) error {
	properties, err := replicationFixtureRequest(instance, http.MethodGet, "/artifactory/api/storage/"+repository+"/selected/current.bin?properties", "", http.StatusOK)
	if err != nil {
		return err
	}
	var propertyView struct {
		Properties map[string][]string `json:"properties"`
	}
	if json.Unmarshal(properties, &propertyView) != nil || len(propertyView.Properties["qualification"]) != 1 || propertyView.Properties["qualification"][0] != expected {
		return fmt.Errorf("ordinary replication property state disagrees with its synchronization option")
	}
	return nil
}

func replicationBytes(instance *world, repository, path, expected string) error {
	actual, err := replicationFixtureRequest(instance, http.MethodGet, "/artifactory/"+repository+"/"+path, "", http.StatusOK)
	if err != nil {
		return err
	}
	if string(actual) != expected {
		return fmt.Errorf("ordinary replication destination bytes differ from the selected source")
	}
	metadata, err := replicationFixtureRequest(instance, http.MethodGet, "/artifactory/api/storage/"+repository+"/"+path, "", http.StatusOK)
	if err != nil {
		return err
	}
	var info struct {
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
	}
	if json.Unmarshal(metadata, &info) != nil || info.Checksums.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(expected))) {
		return fmt.Errorf("ordinary replication destination checksum disagrees with its verified bytes")
	}
	return nil
}

func (w *world) executeAndWaitReplication(repository string) error {
	return w.executeAndWaitReplicationPath(repository, "selected")
}

func (w *world) executeAndWaitReplicationPath(repository, root string) error {
	if _, err := replicationFixtureRequest(w, http.MethodPost, "/artifactory/api/replication/execute/"+repository+"/"+root, "[]", http.StatusAccepted); err != nil {
		return err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		body, err := replicationFixtureRequest(w, http.MethodGet, "/artifactory/api/replication/"+repository, "", http.StatusOK)
		if err != nil {
			return err
		}
		var status struct {
			Status  string `json:"status"`
			Targets []struct {
				Status string `json:"status"`
			} `json:"targets"`
		}
		if json.Unmarshal(body, &status) != nil || strings.Contains(string(body), replicationTransportPassword) {
			return fmt.Errorf("replication status is invalid or discloses a transport credential")
		}
		current := status.Status
		if len(status.Targets) == 1 {
			current = status.Targets[0].Status
		}
		switch current {
		case "ok":
			return nil
		case "incomplete":
		case "error", "partial_failure", "inconsistent":
			return fmt.Errorf("ordinary replication reported %s instead of completing actual effects", current)
		default:
			return fmt.Errorf("ordinary replication reported unexpected status %q", current)
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return fmt.Errorf("ordinary replication did not settle within its fixture deadline")
		case <-w.t.Context().Done():
			return fmt.Errorf("ordinary replication fixture was cancelled")
		}
	}
}
