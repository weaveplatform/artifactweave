package cdn

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
)

// CloudFront hash algorithms for CloudFrontConfig.Hash.
const (
	HashSHA1   = "sha1"
	HashSHA256 = "sha256"
)

// CloudFrontMaxSize is the largest GET response CloudFront caches: AWS
// lists "Maximum cacheable file size per HTTP GET response" as 50 GB.
// Artifactory answered a larger file requested through CloudFront with 400
// until 7.104.5 (RTDEV-49674), so a redirect sends such a file to the object
// store instead. The decimal reading of 50 GB is the smaller one, so no file
// over the limit is sent to the edge whichever unit AWS means.
const CloudFrontMaxSize int64 = 50_000_000_000

// CloudFrontConfig configures a CloudFront signer.
type CloudFrontConfig struct {
	// Domain is the distribution host (see ParseDomain).
	Domain string
	// KeyPairID is the ID of the public key, in one of the distribution's
	// trusted key groups, that matches PrivateKey.
	KeyPairID string
	// PrivateKey is the PEM private key: RSA as PKCS#1 or PKCS#8, or ECDSA
	// as SEC 1 or PKCS#8.
	PrivateKey []byte
	// Hash is HashSHA1 (the default) or HashSHA256.
	Hash string
	// PathPrefix is the distribution origin's path inside the bucket; empty
	// when the origin is the bucket root.
	PathPrefix string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// CloudFront signs canned-policy URLs for a CloudFront distribution. The
// distribution's cache policy must key on the path plus
// response-content-disposition and response-content-type only: never the
// signature parameters, or every URL would be a cache miss, and always the
// overrides, or one item would be served with another's filename.
type CloudFront struct {
	origin origin
	signer *sign.URLSigner
	now    func() time.Time
}

var _ Signer = (*CloudFront)(nil)

// NewCloudFront returns a CloudFront signer.
func NewCloudFront(c CloudFrontConfig) (*CloudFront, error) {
	if c.KeyPairID == "" {
		return nil, fmt.Errorf("%w: cloudfront key_pair_id is required", ErrConfig)
	}
	o, err := newOrigin(c.Domain, c.PathPrefix)
	if err != nil {
		return nil, err
	}
	key, err := ParsePrivateKey(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	signer := sign.NewURLSigner(c.KeyPairID, key)
	switch strings.ToLower(c.Hash) {
	case "", HashSHA1:
	case HashSHA256:
		signer.HashAlg = sign.HashSHA256
	default:
		return nil, fmt.Errorf("%w: cloudfront hash %q is not sha1 or sha256", ErrConfig, c.Hash)
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return &CloudFront{origin: o, signer: signer, now: now}, nil
}

// Provider names the CDN.
func (*CloudFront) Provider() string { return "cloudfront" }

// Covers checks the distribution's origin reaches every key under
// storePrefix.
func (c *CloudFront) Covers(storePrefix string) error {
	return c.origin.covers(c.Provider(), storePrefix)
}

// Sign returns a canned-policy signed URL for objectKey.
func (c *CloudFront) Sign(objectKey string, r Request) (string, error) {
	expires, err := Expiry(c.now(), r.TTL)
	if err != nil {
		return "", err
	}
	raw, err := c.origin.url(objectKey, r)
	if err != nil {
		return "", err
	}
	signed, err := c.signer.Sign(raw, expires)
	if err != nil {
		return "", fmt.Errorf("cdn: cloudfront sign %s: %w", objectKey, err)
	}
	return signed, nil
}

// ParsePrivateKey reads a PEM RSA or ECDSA private key in any of the forms
// AWS and OpenSSL write: PKCS#1, SEC 1 or PKCS#8.
func ParsePrivateKey(pemBytes []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: private key is not PEM", ErrConfig)
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: private key is not PKCS#1, SEC 1 or PKCS#8", ErrConfig)
	}
	switch s := k.(type) {
	case *rsa.PrivateKey:
		return s, nil
	case *ecdsa.PrivateKey:
		return s, nil
	default:
		return nil, fmt.Errorf("%w: private key is %T; CloudFront takes RSA or ECDSA", ErrConfig, k)
	}
}
