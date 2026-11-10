package wire

// ProxyConfiguration is the non-secret Artifactory descriptor document stored
// separately from its sealed password. The remote resolver supports HTTP Basic
// proxies; NTLM-specific fields remain explicit unsupported configurations.
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
