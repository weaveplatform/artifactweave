package source

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// MaxModuleVanityDocumentBytes bounds the complete upstream response before a
// format adapter validates its selected source and admits the private control.
const MaxModuleVanityDocumentBytes = 16 << 20

// ModuleVanityResponse is a caller-independent private representation. A valid
// X-Terraform-Get header selects Source; otherwise HTML retains the exact body.
// HTML uses JSON's byte encoding so a bounded document cannot expand sixfold
// through escaping. Neither field may be delivered as a public module location.
type ModuleVanityResponse struct {
	Source   string `json:"source,omitempty"`
	HTML     []byte `json:"html,omitempty"`
	Identity string `json:"identity"`
}

// ModuleVanityIdentity identifies a logical document without retaining its
// private query. Getter controls do not distinguish requests for that document.
func ModuleVanityIdentity(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return "", ErrInvalidRequest
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ErrInvalidRequest
	}
	for _, control := range []string{"archive", "checksum", "terraform-get"} {
		query.Del(control)
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.RawQuery = query.Encode()
	digest := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(digest[:]), nil
}
