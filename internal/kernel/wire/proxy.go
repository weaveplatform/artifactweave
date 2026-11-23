package wire

import (
	"encoding/json"
	"errors"
	"strings"
)

var errProxyConfiguration = errors.New("proxy: invalid configuration")

// ProxyConfiguration is the non-secret Artifactory descriptor document stored
// separately from its sealed password. The remote resolver and replication push
// support HTTP(S) Basic and NTLMv2 proxies; redirectedToHosts scopes reuse of
// the proxy credentials at challenged remote redirect hosts.
type ProxyConfiguration struct {
	Host              string   `json:"host"`
	Port              int      `json:"port"`
	Username          string   `json:"username"`
	PlatformDefault   bool     `json:"platformDefault"`
	Services          []string `json:"services"`
	Domain            string   `json:"domain"`
	NTHost            string   `json:"ntHost"`
	RedirectedToHosts string   `json:"redirectedToHosts"`
}

// UnmarshalJSON reads descriptor/provider service strings and historical stored
// lists without changing the stored document or the consumers' selection rules.
// Malformed shapes return a redacted error and leave p unchanged.
func (p *ProxyConfiguration) UnmarshalJSON(data []byte) error {
	type configuration ProxyConfiguration
	var decoded configuration
	document := struct {
		*configuration
		Services any `json:"services"`
	}{configuration: &decoded}
	if err := json.Unmarshal(data, &document); err != nil {
		return errProxyConfiguration
	}
	switch services := document.Services.(type) {
	case nil:
	case string:
		// Artifactory's ProxyDescriptor.getServicesList splits on these four
		// characters; other whitespace and list entries retain their meaning.
		decoded.Services = strings.FieldsFunc(services, func(r rune) bool {
			return strings.ContainsRune("\n,; ", r)
		})
	case []any:
		decoded.Services = make([]string, 0, len(services))
		for _, service := range services {
			name, ok := service.(string)
			if !ok {
				return errProxyConfiguration
			}
			decoded.Services = append(decoded.Services, name)
		}
	default:
		return errProxyConfiguration
	}
	*p = ProxyConfiguration(decoded)
	return nil
}
