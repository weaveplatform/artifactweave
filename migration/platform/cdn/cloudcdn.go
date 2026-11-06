package cdn

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Cloud CDN signs URLs with HMAC-SHA1 only
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CloudCDNKeySize is the length of a Cloud CDN signing key: 128 bits.
const CloudCDNKeySize = 16

// cloudCDNKeyName is a key name as Compute Engine accepts it (RFC 1035, 1 to
// 63 characters). It is appended to URLs unescaped, as Google's signing
// sample does, so a name outside this set would sign one URL and send
// another.
var cloudCDNKeyName = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,61}[a-z0-9])?$`)

// CloudCDNConfig configures a Cloud CDN signer.
type CloudCDNConfig struct {
	// Domain is the load balancer host (see ParseDomain).
	Domain string
	// KeyName names the key on the backend bucket.
	KeyName string
	// Key is the key file's contents: 128 random bits, base64url, as
	// "gcloud compute backend-buckets add-signed-url-key" reads them.
	Key []byte
	// PathPrefix is the path inside the bucket that the load balancer maps
	// to its root; empty when it serves the bucket root.
	PathPrefix string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// CloudCDN signs URLs for a Cloud CDN backend bucket. It signs each URL
// whole rather than a URLPrefix: a prefix signature would open every blob
// under the prefix to whoever holds one URL.
//
// Cloud CDN does not refuse unsigned requests; the bucket must be private,
// readable only by the cloud-cdn-fill service account, so that the origin
// refuses them (decision 0029).
type CloudCDN struct {
	origin  origin
	keyName string
	key     []byte
	now     func() time.Time
}

var _ Signer = (*CloudCDN)(nil)

// NewCloudCDN returns a Cloud CDN signer.
func NewCloudCDN(c CloudCDNConfig) (*CloudCDN, error) {
	if !cloudCDNKeyName.MatchString(c.KeyName) {
		return nil, fmt.Errorf(
			"%w: cloud_cdn key_name %q must be 1 to 63 lowercase letters, digits or hyphens, starting with a letter",
			ErrConfig,
			c.KeyName,
		)
	}
	o, err := newOrigin(c.Domain, c.PathPrefix)
	if err != nil {
		return nil, err
	}
	key, err := ParseCloudCDNKey(c.Key)
	if err != nil {
		return nil, err
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return &CloudCDN{origin: o, keyName: c.KeyName, key: key, now: now}, nil
}

// ParseCloudCDNKey decodes a Cloud CDN key file: base64url, with or without
// padding, around 16 raw bytes.
func ParseCloudCDNKey(file []byte) ([]byte, error) {
	s := strings.TrimSpace(string(file))
	for _, enc := range []*base64.Encoding{base64.URLEncoding, base64.RawURLEncoding} {
		key, err := enc.DecodeString(s)
		if err != nil {
			continue
		}
		if len(key) != CloudCDNKeySize {
			return nil, fmt.Errorf(
				"%w: a Cloud CDN key is %d bytes, not %d",
				ErrConfig,
				CloudCDNKeySize,
				len(key),
			)
		}
		return key, nil
	}
	return nil, fmt.Errorf("%w: the Cloud CDN key is not base64url", ErrConfig)
}

// Provider names the CDN.
func (*CloudCDN) Provider() string { return "cloud_cdn" }

// Covers checks the load balancer's path reaches every key under
// storePrefix.
func (c *CloudCDN) Covers(storePrefix string) error {
	return c.origin.covers(c.Provider(), storePrefix)
}

// Sign returns a signed URL for objectKey.
func (c *CloudCDN) Sign(objectKey string, r Request) (string, error) {
	expires, err := Expiry(c.now(), r.TTL)
	if err != nil {
		return "", err
	}
	raw, err := c.origin.url(objectKey, r)
	if err != nil {
		return "", err
	}
	return SignCloudCDN(raw, c.keyName, c.key, expires), nil
}

// SignCloudCDN signs rawURL as Google's documented algorithm does: append
// Expires and KeyName, take the HMAC-SHA1 of the whole URL with the raw key,
// and append it as a padded base64url Signature. Any query the URL already
// has, such as the response overrides, is part of what is signed.
func SignCloudCDN(rawURL, keyName string, key []byte, expires time.Time) string {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	toSign := rawURL + sep + "Expires=" + strconv.FormatInt(expires.Unix(), 10) +
		"&KeyName=" + keyName
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(toSign))
	return toSign + "&Signature=" + base64.URLEncoding.EncodeToString(mac.Sum(nil))
}
