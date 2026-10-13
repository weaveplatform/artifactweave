package azure

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	azblobpkg "github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// storageScope is the Entra ID scope for Azure Storage.
const storageScope = "https://storage.azure.com/.default"

// sourceSASLifetime bounds the SAS that authorises one commit's copy source.
const sourceSASLifetime = time.Hour

// ClientConfig mirrors the blob.azure section of the application
// configuration. Exactly one way to authenticate applies, in this order:
// ConnectionString, AccountKey, SASToken, Credential, and otherwise Entra ID
// through managed identity (ManagedIdentityClientID) or
// DefaultAzureCredential.
type ClientConfig struct {
	// ServiceURL is the Blob service endpoint, for example
	// "https://acct.blob.core.windows.net/" or, for Azurite,
	// "http://127.0.0.1:10000/devstoreaccount1/". Empty means the public
	// endpoint of Account.
	ServiceURL string
	// Container must already exist.
	Container string
	// Account names the storage account.
	Account string
	// AccountKey enables shared-key authentication and service SAS.
	AccountKey string
	// ConnectionString carries the endpoint and credentials together.
	ConnectionString string
	// SASToken authorises every request with a pre-issued SAS; presigning
	// is then unavailable.
	SASToken string
	// Credential is an Entra ID credential to use instead of the defaults.
	Credential azcore.TokenCredential
	// ManagedIdentityClientID selects a user-assigned managed identity.
	ManagedIdentityClientID string
	// SASStartOffset backdates SAS start times against clock skew; zero
	// means 15 minutes, as JFrog does.
	SASStartOffset time.Duration
	// Options configure the SDK pipeline (retries, transport).
	Options *container.ClientOptions
}

// sdkAPI implements API over the Azure SDK.
type sdkAPI struct {
	cc        *container.Client
	container string
	key       *container.SharedKeyCredential
	sasToken  string
	cred      azcore.TokenCredential
	svc       *service.Client
	start     time.Duration
	now       func() time.Time

	mu  sync.Mutex
	udk *sas.UserDelegationCredential
	exp time.Time
}

// NewAPI builds the SDK client for cfg. It makes no request.
func NewAPI(cfg ClientConfig) (API, error) {
	if cfg.Container == "" {
		return nil, fmt.Errorf("%w: container is required", ErrConfig)
	}
	a := &sdkAPI{container: cfg.Container, start: cfg.SASStartOffset, now: time.Now}
	if a.start == 0 {
		a.start = 15 * time.Minute
	}
	opts := cfg.Options
	if opts == nil {
		opts = &container.ClientOptions{}
	}
	if cfg.ConnectionString != "" {
		cc, err := container.NewClientFromConnectionString(
			cfg.ConnectionString,
			cfg.Container,
			opts,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: connection string: %w", ErrConfig, err)
		}
		a.cc = cc
		if name, key := connectionKey(cfg.ConnectionString); name != "" && key != "" {
			if a.key, err = container.NewSharedKeyCredential(name, key); err != nil {
				return nil, fmt.Errorf("%w: connection string key: %w", ErrConfig, err)
			}
		}
		return a, nil
	}
	serviceURL := cfg.ServiceURL
	if serviceURL == "" {
		if cfg.Account == "" {
			return nil, fmt.Errorf("%w: account or service URL is required", ErrConfig)
		}
		serviceURL = "https://" + cfg.Account + ".blob.core.windows.net/"
	}
	if !strings.HasSuffix(serviceURL, "/") {
		serviceURL += "/"
	}
	containerURL := serviceURL + url.PathEscape(cfg.Container)
	var err error
	switch {
	case cfg.AccountKey != "":
		if cfg.Account == "" {
			return nil, fmt.Errorf("%w: an account key needs the account name", ErrConfig)
		}
		if a.key, err = container.NewSharedKeyCredential(cfg.Account, cfg.AccountKey); err != nil {
			return nil, fmt.Errorf("%w: account key: %w", ErrConfig, err)
		}
		a.cc, err = container.NewClientWithSharedKeyCredential(containerURL, a.key, opts)
	case cfg.SASToken != "":
		a.sasToken = strings.TrimPrefix(cfg.SASToken, "?")
		a.cc, err = container.NewClientWithNoCredential(containerURL+"?"+a.sasToken, opts)
	default:
		a.cred = cfg.Credential
		if a.cred == nil {
			if a.cred, err = entraCredential(cfg.ManagedIdentityClientID); err != nil {
				return nil, err
			}
		}
		a.cc, err = container.NewClient(containerURL, a.cred, opts)
		if err == nil {
			a.svc, err = service.NewClient(serviceURL, a.cred, (*service.ClientOptions)(opts))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return a, nil
}

// entraCredential returns the managed identity named by clientID, or the
// default credential chain (environment, workload identity, managed
// identity, developer tools).
func entraCredential(clientID string) (azcore.TokenCredential, error) {
	if clientID != "" {
		c, err := azidentity.NewManagedIdentityCredential(
			&azidentity.ManagedIdentityCredentialOptions{
				ID: azidentity.ClientID(clientID),
			},
		)
		if err != nil {
			return nil, fmt.Errorf("%w: managed identity: %w", ErrConfig, err)
		}
		return c, nil
	}
	c, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("%w: default credential: %w", ErrConfig, err)
	}
	return c, nil
}

// connectionKey extracts AccountName and AccountKey from a connection
// string, for signing SAS; the SDK parses the rest.
func connectionKey(cs string) (name, key string) {
	for part := range strings.SplitSeq(cs, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "AccountName":
			name = v
		case "AccountKey":
			key = v
		}
	}
	return name, key
}

// translate maps Azure error codes onto the package and blob sentinels.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case bloberror.HasCode(err, bloberror.ContainerNotFound):
		return fmt.Errorf("%w: container not found: %w", ErrConfig, err)
	case bloberror.HasCode(err, bloberror.BlobNotFound):
		return fmt.Errorf("%w: %w", blob.ErrNotFound, err)
	case bloberror.HasCode(err, bloberror.BlobAlreadyExists, bloberror.ConditionNotMet):
		return fmt.Errorf("%w: %w", ErrPrecondition, err)
	case bloberror.HasCode(err, bloberror.InvalidBlockList):
		return fmt.Errorf("%w: %w", ErrBlockList, err)
	}
	return err
}

func body(data []byte) io.ReadSeekCloser { return streaming.NopCloser(bytes.NewReader(data)) }

func ifNoneMatchAny(on bool) *azblobpkg.AccessConditions {
	if !on {
		return nil
	}
	return &azblobpkg.AccessConditions{
		ModifiedAccessConditions: &azblobpkg.ModifiedAccessConditions{
			IfNoneMatch: to.Ptr(azcore.ETagAny),
		},
	}
}

func (a *sdkAPI) StageBlock(ctx context.Context, name, id string, data []byte) error {
	_, err := a.cc.NewBlockBlobClient(name).StageBlock(ctx, id, body(data), nil)
	return translate(err)
}

func (a *sdkAPI) StageBlockFromURL(
	ctx context.Context, dst, id, src string, offset, count int64, srcETag string,
) error {
	opts := &blockblob.StageBlockFromURLOptions{
		Range: azblobpkg.HTTPRange{Offset: offset, Count: count},
		SourceModifiedAccessConditions: &azblobpkg.SourceModifiedAccessConditions{
			SourceIfMatch: to.Ptr(azcore.ETag(srcETag)),
		},
	}
	source, err := a.sourceURL(ctx, src, opts)
	if err != nil {
		return err
	}
	_, err = a.cc.NewBlockBlobClient(dst).StageBlockFromURL(ctx, id, source, opts)
	return translate(err)
}

// sourceURL authorises reading src for a copy: Shared Key on the request
// does not authorise the source, so it carries a SAS, or under Entra ID a
// bearer token in x-ms-copy-source-authorization (research 18).
func (a *sdkAPI) sourceURL(
	ctx context.Context,
	src string,
	opts *blockblob.StageBlockFromURLOptions,
) (string, error) {
	u := a.cc.NewBlobClient(src).URL()
	switch {
	case a.key != nil:
		qp, err := a.serviceSAS(src, sourceSASLifetime)
		if err != nil {
			return "", err
		}
		return u + "?" + qp, nil
	case a.sasToken != "":
		// The container client was built on a URL carrying the SAS, so
		// its blob URLs carry it already; appending it again breaks the
		// source's signature (CannotVerifyCopySource).
		return u, nil
	case a.cred != nil:
		tok, err := a.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{storageScope}})
		if err != nil {
			return "", fmt.Errorf("blob/azure: copy source token: %w", err)
		}
		opts.CopySourceAuthorization = to.Ptr("Bearer " + tok.Token)
		return u, nil
	}
	return "", fmt.Errorf("%w: no credential can authorise a copy source", ErrConfig)
}

func (a *sdkAPI) CommitBlockList(
	ctx context.Context, name string, ids []string, md5 []byte, ifNoneMatch bool,
) (string, error) {
	resp, err := a.cc.NewBlockBlobClient(name).
		CommitBlockList(ctx, ids, &blockblob.CommitBlockListOptions{
			HTTPHeaders:      &azblobpkg.HTTPHeaders{BlobContentMD5: md5},
			AccessConditions: ifNoneMatchAny(ifNoneMatch),
		})
	if err != nil {
		return "", translate(err)
	}
	return string(*resp.ETag), nil
}

func (a *sdkAPI) Upload(ctx context.Context, name string, data []byte, ifNoneMatch bool) error {
	_, err := a.cc.NewBlockBlobClient(name).Upload(ctx, body(data), &blockblob.UploadOptions{
		AccessConditions: ifNoneMatchAny(ifNoneMatch),
	})
	return translate(err)
}

func (a *sdkAPI) Properties(ctx context.Context, name string) (Props, error) {
	resp, err := a.cc.NewBlobClient(name).GetProperties(ctx, nil)
	if err != nil {
		return Props{}, translate(err)
	}
	p := Props{MD5: resp.ContentMD5}
	if resp.ContentLength != nil {
		p.Size = *resp.ContentLength
	}
	if resp.ETag != nil {
		p.ETag = string(*resp.ETag)
	}
	return p, nil
}

func (a *sdkAPI) Download(
	ctx context.Context,
	name string,
	offset, count int64,
	etag string,
) (io.ReadCloser, error) {
	opts := &azblobpkg.DownloadStreamOptions{
		Range: azblobpkg.HTTPRange{Offset: offset, Count: count},
	}
	if etag != "" {
		opts.AccessConditions = &azblobpkg.AccessConditions{
			ModifiedAccessConditions: &azblobpkg.ModifiedAccessConditions{
				IfMatch: to.Ptr(azcore.ETag(etag)),
			},
		}
	}
	resp, err := a.cc.NewBlobClient(name).DownloadStream(ctx, opts)
	if err != nil {
		return nil, translate(err)
	}
	return resp.NewRetryReader(ctx, &azblobpkg.RetryReaderOptions{MaxRetries: 3}), nil
}

func (a *sdkAPI) Delete(ctx context.Context, name string) error {
	_, err := a.cc.NewBlobClient(name).Delete(ctx, &azblobpkg.DeleteOptions{
		DeleteSnapshots: to.Ptr(azblobpkg.DeleteSnapshotsOptionTypeInclude),
	})
	return translate(err)
}

func (a *sdkAPI) List(ctx context.Context, prefix, marker string) ([]Item, string, error) {
	opts := &container.ListBlobsFlatOptions{Prefix: &prefix}
	if marker != "" {
		opts.Marker = &marker
	}
	page, err := a.cc.NewListBlobsFlatPager(opts).NextPage(ctx)
	if err != nil {
		return nil, "", translate(err)
	}
	var items []Item
	if page.Segment != nil {
		for _, b := range page.Segment.BlobItems {
			it := Item{Name: *b.Name}
			if b.Properties != nil && b.Properties.ContentLength != nil {
				it.Size = *b.Properties.ContentLength
			}
			items = append(items, it)
		}
	}
	next := ""
	if page.NextMarker != nil {
		next = *page.NextMarker
	}
	return items, next, nil
}

func (a *sdkAPI) SignRead(ctx context.Context, name string, ttl time.Duration) (string, error) {
	u := a.cc.NewBlobClient(name).URL()
	switch {
	case a.key != nil:
		qp, err := a.serviceSAS(name, ttl)
		if err != nil {
			return "", err
		}
		return u + "?" + qp, nil
	case a.svc != nil:
		qp, err := a.delegationSAS(ctx, name, ttl)
		if err != nil {
			return "", err
		}
		return u + "?" + qp, nil
	}
	return "", fmt.Errorf(
		"%w: presigning needs an account key or an Entra ID credential",
		ErrConfig,
	)
}

// readValues is a read-only SAS for one blob. The container and blob names
// are given explicitly: the SDK's GetSASURL re-parses the URL and signs the
// wrong blob name on hosts that are not an IP address or *.blob.* (research
// 18).
func (a *sdkAPI) readValues(name string, ttl time.Duration) sas.BlobSignatureValues {
	now := a.now().UTC()
	return sas.BlobSignatureValues{
		Protocol:      sas.ProtocolHTTPSandHTTP,
		StartTime:     now.Add(-a.start),
		ExpiryTime:    now.Add(ttl),
		Permissions:   (&sas.BlobPermissions{Read: true}).String(),
		ContainerName: a.container,
		BlobName:      name,
	}
}

func (a *sdkAPI) serviceSAS(name string, ttl time.Duration) (string, error) {
	qp, err := a.readValues(name, ttl).SignWithSharedKey(a.key)
	if err != nil {
		return "", fmt.Errorf("blob/azure: sign %s: %w", name, err)
	}
	return qp.Encode(), nil
}

// delegationSAS signs with a user delegation key, fetched once and reused
// until an hour before it expires. A key lives at most seven days.
func (a *sdkAPI) delegationSAS(
	ctx context.Context,
	name string,
	ttl time.Duration,
) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	if a.udk == nil || !now.Add(ttl+time.Hour).Before(a.exp) {
		exp := now.Add(7 * 24 * time.Hour)
		info := service.KeyInfo{
			Start:  to.Ptr(now.Add(-a.start).Format(sas.TimeFormat)),
			Expiry: to.Ptr(exp.Format(sas.TimeFormat)),
		}
		udk, err := a.svc.GetUserDelegationCredential(ctx, info, nil)
		if err != nil {
			return "", fmt.Errorf("blob/azure: user delegation key: %w", translate(err))
		}
		a.udk, a.exp = udk, exp
	}
	qp, err := a.readValues(name, ttl).SignWithUserDelegation(a.udk)
	if err != nil {
		return "", fmt.Errorf("blob/azure: sign %s: %w", name, err)
	}
	return qp.Encode(), nil
}

var _ API = (*sdkAPI)(nil)
