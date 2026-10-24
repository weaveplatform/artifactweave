package azure

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// testKey is a syntactically valid account key; nothing verifies it.
var testKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 64)))

func TestUnitTranslate(t *testing.T) {
	for code, want := range map[string]error{
		"ContainerNotFound": ErrConfig,
		"BlobNotFound":      blob.ErrNotFound,
		"BlobAlreadyExists": ErrPrecondition,
		"ConditionNotMet":   ErrPrecondition,
		"InvalidBlockList":  ErrBlockList,
	} {
		if err := translate(&azcore.ResponseError{ErrorCode: code}); !errors.Is(err, want) {
			t.Errorf("%s: %v", code, err)
		}
	}
	other := &azcore.ResponseError{ErrorCode: "ServerBusy"}
	if err := translate(other); !errors.Is(err, other) || errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("other codes pass through: %v", err)
	}
	if translate(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

func TestUnitConnectionKey(t *testing.T) {
	name, key := connectionKey(
		"DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=a2V5=;junk;EndpointSuffix=core.windows.net",
	)
	if name != "acct" || key != "a2V5=" {
		t.Fatalf("%q %q", name, key)
	}
}

type staticCred struct{ calls atomic.Int64 }

func (c *staticCred) GetToken(
	context.Context,
	policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	c.calls.Add(1)
	return azcore.AccessToken{Token: "tok", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type failingCred struct{}

func (failingCred) GetToken(
	context.Context,
	policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	return azcore.AccessToken{}, errInjected
}

func TestUnitNewAPIConfigurations(t *testing.T) {
	for name, cfg := range map[string]ClientConfig{
		"no container":     {Account: "a", AccountKey: testKey},
		"no endpoint":      {Container: "c"},
		"key without name": {ServiceURL: "https://a.blob.core.windows.net", Container: "c", AccountKey: testKey},
		"key not base64":   {Account: "a", Container: "c", AccountKey: "%%%"},
		"bad conn string":  {Container: "c", ConnectionString: "nonsense"},
		"bad key in conn":  {Container: "c", ConnectionString: "AccountName=a;AccountKey=%%%;BlobEndpoint=https://a.blob.core.windows.net/"},
	} {
		if _, err := NewAPI(cfg); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, cfg := range map[string]ClientConfig{
		"account key":      {Account: "acct", Container: "c", AccountKey: testKey},
		"connection":       {Container: "c", ConnectionString: "AccountName=acct;AccountKey=" + testKey + ";BlobEndpoint=https://acct.blob.core.windows.net/"},
		"sas token":        {Account: "acct", Container: "c", SASToken: "?sv=x&sig=y"},
		"credential":       {ServiceURL: "https://acct.blob.core.windows.net", Container: "c", Credential: &staticCred{}},
		"managed identity": {Account: "acct", Container: "c", ManagedIdentityClientID: "00000000-0000-0000-0000-000000000001"},
		"default chain":    {Account: "acct", Container: "c"},
	} {
		api, err := NewAPI(cfg)
		if err != nil || api == nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	a, _ := NewAPI(
		ClientConfig{Container: "c", ConnectionString: "AccountName=acct;AccountKey=" + testKey +
			";BlobEndpoint=https://acct.blob.core.windows.net/"},
	)
	if a.(*sdkAPI).key == nil { //nolint:forcetypeassert // the concrete adapter under test
		t.Fatal("a connection string with a key must enable signing")
	}
}

func TestUnitCanSign(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  ClientConfig
		want bool
	}{
		{"shared key", ClientConfig{AccountKey: testKey}, true},
		{"connection string with a key", ClientConfig{ConnectionString: "AccountName=a;AccountKey=" + testKey}, true},
		{"connection string with a SAS", ClientConfig{ConnectionString: "BlobEndpoint=https://a/;SharedAccessSignature=sv=x"}, false},
		{"pre-issued SAS token", ClientConfig{SASToken: "sv=x&sig=y"}, false},
		{"Entra ID", ClientConfig{Credential: &staticCred{}}, true},
		{"managed identity", ClientConfig{ManagedIdentityClientID: "id"}, true},
	} {
		if got := c.cfg.CanSign(); got != c.want {
			t.Errorf("%s: CanSign = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestUnitServiceSAS(t *testing.T) {
	api, err := NewAPI(ClientConfig{Account: "acct", Container: "blobs", AccountKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	a := api.(*sdkAPI) //nolint:forcetypeassert // the concrete adapter under test
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	raw, err := a.SignRead(context.Background(), "art/sha256/ab/abc", ReadSAS{
		TTL: time.Hour, ContentDisposition: "attachment", ContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "acct.blob.core.windows.net" || u.Path != "/blobs/art/sha256/ab/abc" {
		t.Fatalf("url %s", raw)
	}
	if q.Get("sr") != "b" || q.Get("sp") != "r" || q.Get("sig") == "" || q.Get("sv") == "" ||
		q.Get("se") != "2026-10-13T13:00:00Z" || q.Get("st") != "2026-10-13T11:45:00Z" ||
		q.Get("rscd") != "attachment" || q.Get("rsct") != "text/plain" {
		t.Fatalf("query %v", q)
	}
}

func TestUnitSourceAuthorisation(t *testing.T) {
	ctx := context.Background()
	keyed, _ := NewAPI(ClientConfig{Account: "acct", Container: "c", AccountKey: testKey})
	opts := &blockblob.StageBlockFromURLOptions{}
	u, err := keyed.(*sdkAPI).sourceURL(
		ctx,
		"tmp",
		opts,
	) //nolint:forcetypeassert // adapter under test
	if err != nil || !strings.Contains(u, "sig=") || opts.CopySourceAuthorization != nil {
		t.Fatalf("shared key source = %q, %v", u, err)
	}
	sasd, _ := NewAPI(ClientConfig{Account: "acct", Container: "c", SASToken: "sv=x&sig=y"})
	if u, err := sasd.(*sdkAPI).sourceURL(
		ctx,
		"tmp",
		opts,
	); err != nil ||
		u != "https://acct.blob.core.windows.net/c/tmp?sv=x&sig=y" { //nolint:forcetypeassert,lll // adapter under test
		// The SAS appears once: the container client's URL already carries
		// it, and a second copy fails the copy source's signature check
		// (CannotVerifyCopySource on real Azure).
		t.Fatalf("sas source = %q, %v", u, err)
	}
	if _, err := sasd.SignRead(ctx, "x", ReadSAS{TTL: time.Minute}); !errors.Is(err, ErrConfig) {
		t.Fatalf("a SAS token must not be handed out: %v", err)
	}
	tokened, _ := NewAPI(ClientConfig{Account: "acct", Container: "c", Credential: &staticCred{}})
	if u, err := tokened.(*sdkAPI).sourceURL(
		ctx,
		"tmp",
		opts,
	); err != nil || strings.Contains(u, "?") || //nolint:forcetypeassert,lll // adapter under test
		opts.CopySourceAuthorization == nil ||
		*opts.CopySourceAuthorization != "Bearer tok" {
		t.Fatalf("bearer source = %q, %v", u, err)
	}
	failing, _ := NewAPI(ClientConfig{Account: "acct", Container: "c", Credential: failingCred{}})
	if _, err := failing.(*sdkAPI).sourceURL(
		ctx,
		"tmp",
		opts,
	); !errors.Is(
		err,
		errInjected,
	) { //nolint:forcetypeassert // adapter under test
		t.Fatal(err)
	}
	bare := &sdkAPI{cc: keyed.(*sdkAPI).cc} //nolint:forcetypeassert // adapter under test
	if _, err := bare.sourceURL(ctx, "tmp", opts); !errors.Is(err, ErrConfig) {
		t.Fatal(err)
	}
	if err := failing.StageBlockFromURL(
		ctx,
		"d",
		"id",
		"tmp",
		0,
		1,
		"e",
	); !errors.Is(
		err,
		errInjected,
	) {
		t.Fatal(err)
	}
}

// TestAccDelegationSAS signs with a user delegation key from a chi test server
// that models Get User Delegation Key, and reuses the key.
func TestAccDelegationSAS(t *testing.T) {
	var keys atomic.Int64
	fail := atomic.Bool{}
	r := chi.NewRouter()
	r.Post("/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("comp") != "userdelegationkey" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		keys.Add(1)
		if fail.Load() {
			w.Header().Set("x-ms-error-code", "AuthorizationPermissionMismatch")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?><UserDelegationKey>` +
			`<SignedOid>oid</SignedOid><SignedTid>tid</SignedTid>` +
			`<SignedStart>2026-10-13T00:00:00Z</SignedStart><SignedExpiry>2026-10-20T00:00:00Z</SignedExpiry>` +
			`<SignedService>b</SignedService><SignedVersion>2026-12-06</SignedVersion>` +
			`<Value>` + testKey + `</Value></UserDelegationKey>`))
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	opts := &container.ClientOptions{}
	opts.InsecureAllowCredentialWithHTTP = true
	opts.Retry = policy.RetryOptions{MaxRetries: -1}
	api, err := NewAPI(ClientConfig{
		ServiceURL: srv.URL, Container: "c", Credential: &staticCred{}, Options: opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for range 2 {
		raw, err := api.SignRead(ctx, "x", ReadSAS{TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		q, _ := url.ParseQuery(raw[strings.Index(raw, "?")+1:])
		if q.Get("skoid") != "oid" || q.Get("sig") == "" || q.Get("sp") != "r" {
			t.Fatalf("delegation SAS %v", q)
		}
	}
	if keys.Load() != 1 {
		t.Fatalf("user delegation key fetched %d times, want once", keys.Load())
	}
	a := api.(*sdkAPI) //nolint:forcetypeassert // the concrete adapter under test
	a.udk = nil
	fail.Store(true)
	if _, err := api.SignRead(ctx, "x", ReadSAS{TTL: time.Hour}); err == nil {
		t.Fatal("a refused key request must fail the signing")
	}
}
