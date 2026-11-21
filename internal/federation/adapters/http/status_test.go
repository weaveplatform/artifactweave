package federationhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitReplicationStatusPathDecodesOnceAndKeepsLiteralCharacters(t *testing.T) {
	for _, raw := range []string{"libs", "libs/", "libs/com/acme", "libs/a%3Bb%3Fc%23d%25%20space%2B%C3%A9.bin", "libs/%252f.bin"} {
		key, path, err := replicationPath(raw)
		if err != nil || key != "libs" {
			t.Fatal("valid replication path refused", raw, err)
		}
		if raw == "libs/%252f.bin" && path != "%2f.bin" {
			t.Fatal("replication path decoded more than once")
		}
		if raw == "libs/a%3Bb%3Fc%23d%25%20space%2B%C3%A9.bin" && path != "a;b?c#d% space+é.bin" {
			t.Fatal("literal path characters changed", path)
		}
	}
	for _, raw := range []string{"", "libs//file", "libs/a%2Fb", "libs/%5Cprivate", "libs/%zz", "libs/../private", "libs/./file"} {
		if _, _, err := replicationPath(raw); err == nil {
			t.Fatal("ambiguous replication path accepted", raw)
		}
	}
}

func TestUnitReplicationStatusWirePreservesDirectionAndLastCompletion(t *testing.T) {
	at := time.Unix(1, 0).UTC()
	for _, class := range []repo.Class{repo.Local, repo.Remote} {
		wire := statusWire(
			replication.StatusView{
				Class:   class,
				Targets: []replication.TargetStatus{{URL: "https://peer.example/artifactory/target", Status: model.ReplicationIncomplete, LastCompleted: at}},
			},
		)
		if class == repo.Local {
			if wire.Targets == nil || len(*wire.Targets) != 1 || wire.Status != nil || (*wire.Targets)[0].LastCompleted == nil || !(*wire.Targets)[0].LastCompleted.Equal(at) ||
				*(*wire.Targets)[0].RepoKey != "target" {
				t.Fatal("push status shape lost target/completion")
			}
		} else if wire.Targets != nil || wire.Status == nil || *wire.Status != "incomplete" || wire.LastCompleted == nil || !wire.LastCompleted.Equal(at) {
			t.Fatal("pull status shape lost completion")
		}
	}
	body, err := json.Marshal(statusWire(replication.StatusView{Class: repo.Remote}))
	if err != nil || string(body) != `{"status":"never_run"}` {
		t.Fatal("never-run status invented completion", string(body), err)
	}
	response := httptest.NewRecorder()
	ReplicationAPI{}.getStatus(response, httptest.NewRequest(http.MethodGet, "/artifactory/api/replication/libs", nil))
	if response.Code != http.StatusNotImplemented {
		t.Fatal("unbound status route claimed implementation")
	}
}

func TestUnitReplicationExecuteRejectsAmbiguousBodiesAndUndeliveredOptions(t *testing.T) {
	for _, raw := range []string{"", "[]", "[{}]"} {
		if err := executionBody(strings.NewReader(raw)); err != nil {
			t.Fatal("stored execution body refused", err)
		}
	}
	for _, raw := range []string{"null", "{}", "[{},{}]", "[null,null]", "[null]", `[{"unknown":true}]`, "[]{}", "[{broken]", "[{\"unknown\":true}]"} {
		var answer *apierror.Error
		if err := executionBody(strings.NewReader(raw)); !errors.As(err, &answer) || answer.Status != http.StatusBadRequest {
			t.Fatal("ambiguous execution body accepted", raw, err)
		}
	}
	if err := executionBody(strings.NewReader(`[{"password":null}]`)); !errors.Is(err, apierror.ErrNotImplemented) {
		t.Fatal("explicit null override was silently discarded", err)
	}
	if err := executionBody(strings.NewReader(`[{"password":"private-must-not-be-ignored"}]`)); !errors.Is(err, apierror.ErrNotImplemented) {
		t.Fatal("one-shot options silently discarded", err)
	}
}
