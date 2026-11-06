package cdn

import (
	"fmt"
	"strings"
	"time"
)

// FrontDoorConfig configures a FrontDoor.
type FrontDoorConfig struct {
	// Endpoint is the Front Door endpoint host, such as
	// "artifacts-abc123.z01.azurefd.net" (see ParseDomain).
	Endpoint string
	// PathPrefix is the route's origin path: the leading part of
	// "<container>/<blob name>" that the endpoint leaves out, for example the
	// container when the origin path is "/<container>". Empty when the
	// endpoint path is the whole storage path.
	PathPrefix string
}

// FrontDoor places read SAS URLs on an Azure Front Door endpoint (decision
// 0029). Front Door checks no signature of its own: it forwards the request,
// query string included, to the storage origin, which checks the SAS. So
// FrontDoor signs nothing and is not a Signer: the azure store mints a
// window-aligned SAS with its own credentials, which may need a network call
// for a user delegation key, and FrontDoor moves it onto the endpoint.
//
// The route must key its cache on the full query string
// (query_string_caching_behavior = UseQueryString), or a cached private
// blob is served to anyone who asks for its path, and must cap cache
// expiry at the SAS window, because Front Door keeps serving a cached
// object after its SAS has expired.
type FrontDoor struct {
	origin origin
}

// NewFrontDoor returns a FrontDoor.
func NewFrontDoor(c FrontDoorConfig) (*FrontDoor, error) {
	o, err := newOrigin(c.Endpoint, c.PathPrefix)
	if err != nil {
		return nil, err
	}
	return &FrontDoor{origin: o}, nil
}

// Provider names the CDN.
func (*FrontDoor) Provider() string { return "front_door" }

// Covers checks the endpoint reaches every blob under storagePrefix, the
// store's "<container>/<prefix>".
func (f *FrontDoor) Covers(storagePrefix string) error {
	return f.origin.covers(f.Provider(), storagePrefix)
}

// URL returns the endpoint URL for the blob at storagePath,
// "<container>/<blob name>", carrying sasQuery, the SAS's encoded query
// string, unchanged: the origin checks the signature over the storage path,
// which the edge forwards as it is.
func (f *FrontDoor) URL(storagePath, sasQuery string) (string, error) {
	rest, ok := strings.CutPrefix(storagePath, f.origin.prefix)
	if !ok {
		return "", fmt.Errorf(
			"%w: storage path %q is outside path_prefix %q",
			ErrConfig,
			storagePath,
			f.origin.prefix,
		)
	}
	u := *f.origin.base
	u.Path = "/" + rest
	u.RawQuery = sasQuery
	return u.String(), nil
}

// Window returns the window now falls in for ttl and the expiry of a URL
// issued in it, as Expiry does: windows are ttl long, start on a multiple of
// ttl since the Unix epoch, and a URL issued in one expires at its end plus
// ttl. A signer that fixes its start time to the window start, as an Azure
// SAS does, then issues the same URL for a blob throughout the window.
func Window(now time.Time, ttl time.Duration) (start, expiry time.Time, err error) {
	expiry, err = Expiry(now, ttl)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	sec := int64(ttl / time.Second)
	return expiry.Add(-2 * time.Duration(sec) * time.Second), expiry, nil
}
