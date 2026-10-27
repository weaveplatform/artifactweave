package huggingface

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	format "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestUnitRemoteMetadataRejectsIdentityPathAndDigestFaults(t *testing.T) {
	id, _ := format.ParseIdentity("models", "acme", "demo")
	base := `{"id":"acme/demo","sha":"0123456789012345678901234567890123456789","siblings":[{"rfilename":"config.json","size":2,"blobId":"0123456789012345678901234567890123456789"}]}`
	if _, e := decodeInfo(strings.NewReader(base), int64(len(base)), id); e != nil {
		t.Fatal(e)
	}
	for _, body := range []string{
		strings.Replace(base, `"id":"acme/demo"`, `"id":"other/demo"`, 1),
		strings.Replace(base, `"rfilename":"config.json"`, `"rfilename":"../secret"`, 1),
		strings.Replace(base, `,"blobId":"0123456789012345678901234567890123456789"`, ``, 1),
		strings.Replace(base, `"blobId":"0123456789012345678901234567890123456789"`, `"blobId":"not-a-git-digest"`, 1),
		strings.Replace(base, `"blobId":"0123456789012345678901234567890123456789"`, `"blobId":"`+strings.Repeat("z", 40)+`"`, 1),
		strings.Replace(base, `"size":2`, `"size":-1`, 1),
	} {
		if _, e := decodeInfo(
			strings.NewReader(body),
			int64(len(body)),
			id,
		); !errors.Is(
			e,
			source.ErrInvalidResult,
		) {
			t.Errorf("accepted malformed info %s: %v", body, e)
		}
	}
	if _, e := decodeInfo(
		bytes.NewReader([]byte(base)),
		int64(len(base)+1),
		id,
	); !errors.Is(
		e,
		source.ErrInvalidResult,
	) {
		t.Errorf("accepted body size mismatch: %v", e)
	}
	if _, e := decodeInfo(strings.NewReader(""), 0, id); !errors.Is(e, source.ErrInvalidResult) {
		t.Errorf("accepted empty remote metadata: %v", e)
	}
}

func TestUnitCommitRejectsTraversalAndDuplicate(t *testing.T) {
	for _, path := range []string{"../secret", "a/../b", "/absolute"} {
		body := `{"key":"header","value":{"summary":"x"}}` + "\n" + `{"key":"file","value":{"path":"` + path + `","content":"e30=","encoding":"base64"}}` + "\n"
		if _, e := parseCommit([]byte(body)); e == nil {
			t.Errorf("accepted %q", path)
		}
	}
	line := `{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}`
	body := `{"key":"header","value":{"summary":"x"}}` + "\n" + line + "\n" + line + "\n"
	if _, e := parseCommit([]byte(body)); e == nil {
		t.Fatal("accepted duplicate")
	}
	if _, e := parseCommit([]byte(`{"key":"header","value":{}}`)); e == nil {
		t.Fatal("accepted a commit without a file operation")
	}
}

func TestUnitDocumentedHuggingFaceCDNHosts(t *testing.T) {
	for _, host := range []string{"cas-server.xethub.hf.co", "cas-server.xethub-eu.hf.co", "transfer.xethub.hf.co", "transfer.xethub-eu.hf.co", "us.aws.cdn.hf.co", "us.gcp.cdn.hf.co", "cdn-lfs-us-1.hf.co", "cdn-lfs-eu-1.hf.co"} {
		if !huggingFaceCDN(host) {
			t.Errorf("missing documented host %q", host)
		}
	}
	for _, host := range []string{"evil.hf.co.attacker.invalid", "other.hf.co", "huggingface.co.evil.test"} {
		if huggingFaceCDN(host) {
			t.Errorf("accepted unknown host %q", host)
		}
	}
}
