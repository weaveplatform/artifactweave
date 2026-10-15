package platform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The geo-replicated scenarios (#131): one store that the cloud replicates
// to a second region, invisible to artifactweave. These checks look past
// artifactweave at the cloud's copy. A cloud's replication lag is bounded
// only by its recovery point objective (an hour for a default GCS
// dual-region, none for S3 replication without Replication Time Control,
// 15 minutes for Azure geo priority replication; research 20), so a copy
// that has not arrived within replicaWait is reported, not failed. A copy
// that arrives with other content, or a replication the cloud marks as
// failed, fails the check.
const (
	replicaWait = 12 * time.Minute
	replicaPoll = 20 * time.Second
)

func init() {
	scenarioChecks["aws/geo-replicated"] = []check{
		{"the replica bucket receives the blob", awsReplica},
	}
	scenarioChecks["gcp/geo-replicated"] = []check{
		{"the bucket is a dual-region", gcpDualRegion},
	}
	scenarioChecks["azure/geo-replicated"] = []check{
		{"the secondary endpoint serves the blob", azureSecondary},
	}
}

// awsReplica waits for S3 to report the blob replicated, then reads it from
// the replica bucket in the other region.
func awsReplica(t *testing.T, h *harness) {
	primary, region := h.output(t, "bucket"), h.output(t, "region")
	replica, replicaRegion := h.output(t, "replica_bucket"), h.output(t, "replica_region")
	// The signed URL is virtual-hosted, so its path is the object key.
	key := strings.TrimPrefix(redirectLocation(t, h, nil).Path, "/")
	if want := h.output(t, "replicated_prefix"); !strings.HasPrefix(key, want) {
		t.Fatalf("blob key %q is outside the replicated prefix %q", key, want)
	}
	started := time.Now()
	status := ""
	for time.Since(started) < replicaWait {
		out := cli(t, "aws", "s3api", "head-object", "--bucket", primary, "--key", key,
			"--region", region, "--query", "ReplicationStatus", "--output", "text")
		status = strings.TrimSpace(out)
		if status == "COMPLETED" {
			break
		}
		if status == "FAILED" {
			t.Fatalf("S3 marked the blob's replication FAILED: check the replication role and rule")
		}
		time.Sleep(replicaPoll)
	}
	if status != "COMPLETED" {
		t.Skipf("informational: replication status still %q after %s; replication without "+
			"Replication Time Control has no time bound", status, replicaWait)
	}
	var head struct {
		ContentLength     int64  `json:"ContentLength"`
		ReplicationStatus string `json:"ReplicationStatus"`
	}
	out := cli(t, "aws", "s3api", "head-object", "--bucket", replica, "--key", key,
		"--region", replicaRegion, "--output", "json")
	if err := json.Unmarshal([]byte(out), &head); err != nil {
		t.Fatalf("head-object on the replica: %v", err)
	}
	if head.ContentLength != payloadSize || head.ReplicationStatus != "REPLICA" {
		t.Fatalf("replica object: %d bytes, status %q; want %d bytes, REPLICA",
			head.ContentLength, head.ReplicationStatus, payloadSize)
	}
	t.Logf("replicated to %s (%s) within %s", replica, replicaRegion,
		time.Since(started).Round(time.Second))
}

// gcpDualRegion reads the bucket's real placement. GCS exposes no
// per-object replication status, and a dual-region bucket is one bucket, so
// the signed URL the generic checks used already serves every location.
func gcpDualRegion(t *testing.T, h *harness) {
	bucket := h.output(t, "bucket")
	out := cli(t, "gcloud", "storage", "buckets", "describe", "gs://"+bucket, "--format=json")
	var b map[string]any
	if err := json.Unmarshal([]byte(out), &b); err != nil {
		t.Fatalf("describe %s: %v", bucket, err)
	}
	locType := field(b, "location_type", "locationType")
	if locType != "dual-region" {
		t.Fatalf("bucket %s has location type %q, want dual-region", bucket, locType)
	}
	t.Logf("bucket %s: location %s, %s, rpo %s, data locations %v", bucket,
		field(b, "location"), locType, field(b, "rpo"), b["data_locations"])
}

// azureSecondary reads the blob from the paired region's read endpoint with
// a user delegation SAS artifactweave minted for the primary: the SAS signs
// the account and path, not the host, so the same query authorises the
// secondary.
func azureSecondary(t *testing.T, h *harness) {
	secondary := h.output(t, "secondary_storage_host")
	started := time.Now()
	last := ""
	for time.Since(started) < replicaWait {
		// A fresh redirect each time: the SAS lasts the redirect TTL.
		loc := redirectLocation(t, h, nil)
		sec := *loc
		sec.Host = secondary
		resp := fetch(t, h, &sec, nil)
		body := drain(t, resp)
		switch resp.StatusCode {
		case http.StatusOK:
			if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != file.sha256 {
				t.Fatalf("the secondary served %d bytes with other content", len(body))
			}
			t.Logf("secondary %s served the blob %s after it was written to the primary",
				secondary, time.Since(started).Round(time.Second))
			return
		case http.StatusNotFound:
			last = "404 " + resp.Header.Get("X-Ms-Error-Code") + " (not replicated yet)"
		default:
			last = resp.Status + " " + resp.Header.Get("X-Ms-Error-Code") + ": " +
				string(bytes.TrimSpace(body[:min(len(body), 300)]))
		}
		time.Sleep(replicaPoll)
	}
	t.Skipf("informational: the secondary had not served the blob after %s; last answer %s",
		replicaWait, last)
}

// output returns one of the scenario's string outputs.
func (h *harness) output(t *testing.T, name string) string {
	t.Helper()
	raw, ok := h.outputs[name]
	if !ok {
		t.Fatalf("the scenario has no %q output (ARTIFACTWEAVE_SCENARIO_OUTPUTS)", name)
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("output %q is not a string: %v", name, err)
	}
	return v
}

// cli runs a cloud CLI the job has signed in, as the deployer.
func cli(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args[:min(len(args), 3)], " "), err,
			tail(stderr.String(), 2000))
	}
	return string(out)
}

// field returns the first of names present in m as a string.
func field(m map[string]any, names ...string) string {
	for _, n := range names {
		if v, ok := m[n].(string); ok {
			return v
		}
	}
	return ""
}
