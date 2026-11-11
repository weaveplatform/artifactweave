package app

import (
	"log/slog"
	"strings"
)

// serverLog routes net/http's own messages (TLS handshake errors, panics it
// recovered) into slog. It drops the warning net/http logs for every query
// holding a ";": the properties API separates properties with ";", its
// handlers parse the raw query themselves, and http.AllowQuerySemicolons
// would split "k=v;k2=v2" into separate parameters.
type serverLog struct{ log *slog.Logger }

func (l serverLog) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if !strings.Contains(msg, "URL query contains semicolon") {
		l.log.Warn(msg, "source", "net/http")
	}
	return len(p), nil
}
