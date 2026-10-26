package huggingface

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	formathf "github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitRemoteInfoRejectsMalformedBlobIdentity(t *testing.T) {
	id, ok := formathf.ParseIdentity("models", "acme", "demo")
	if !ok {
		t.Fatal("model fixture identity is invalid")
	}
	data := fmt.Sprintf(
		`{"id":"acme/demo","sha":"%s","siblings":[{"rfilename":"config.json","size":2,"blobId":"%s"}]}`,
		strings.Repeat("a", 40),
		strings.Repeat("g", 40),
	)
	if _, err := decodeInfo(
		bytes.NewBufferString(data),
		int64(len(data)),
		id,
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("malformed upstream blob identity accepted: %v", err)
	}
	if hex40("short") {
		t.Fatal("short Git blob identity accepted")
	}
}

type failedJSONBody struct{}

func (failedJSONBody) Read([]byte) (int, error) { return 0, errors.New("client body failed") }
func (failedJSONBody) Close() error             { return nil }

func TestUnitIncompleteMetadataAndRequestBodiesAreRejected(t *testing.T) {
	if _, err := (handler{}).resultBytes(
		t.Context(),
		source.Result{},
	); !errors.Is(
		err,
		source.ErrInvalidResult,
	) {
		t.Fatalf("empty source result accepted: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/models/acme/demo/preupload/main", nil)
	r.Header.Set("Content-Type", "application/json")
	r.Body = failedJSONBody{}
	var body struct{ Files []string }
	if err := boundedJSON(r, 1024, &body); !errors.Is(err, errInvalidJSON) {
		t.Fatalf("failed request body accepted: %v", err)
	}
}

func TestUnitSignedUploadRequiresKeysAndFreshExpiry(t *testing.T) {
	if sig := (handler{}).signedUpload(
		"hf",
		strings.Repeat("a", 64),
		2,
		time.Now().Unix()+60,
	); sig != "" {
		t.Fatalf("unsigned protocol generated a signature: %q", sig)
	}
	keys, err := crypt.New(bytes.Repeat([]byte("h"), 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	h := handler{o: Options{Keys: keys, Now: func() time.Time { return now }}}
	for _, expiry := range []int64{now.Unix() - 1, now.Add(11 * time.Minute).Unix()} {
		request := httptest.NewRequest(http.MethodPut, fmt.Sprintf(
			"/lfs/upload/%s?size=2&exp=%d&sig=aa", strings.Repeat("a", 64), expiry,
		), nil)
		if _, ok := h.verifyUpload(request, strings.Repeat("a", 64)); ok {
			t.Fatalf("out-of-window upload expiry accepted: %d", expiry)
		}
	}
}

func TestUnitVirtualSourceNeverYieldsUnselectedContent(t *testing.T) {
	if _, err := (virtualSource{}).Item(
		t.Context(),
		inmem.New(),
		repo.Config{},
		"models--acme--demo",
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatalf("virtual source yielded unselected content: %v", err)
	}
}
