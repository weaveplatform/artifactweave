package app

import (
	"bytes"
	stdlog "log"
	"log/slog"
	"strings"
	"testing"
)

func TestAccServerLogDropsSemicolonWarning(t *testing.T) {
	var buf bytes.Buffer
	l := stdlog.New(serverLog{slog.New(slog.NewTextHandler(&buf, nil))}, "", 0)
	l.Print("http: URL query contains semicolon, which is no longer a supported separator; " +
		"parts of the query may be stripped when parsed; see golang.org/issue/25192")
	l.Print("http: TLS handshake error from 10.0.0.1:1234: EOF")
	out := buf.String()
	if strings.Contains(out, "semicolon") || !strings.Contains(out, "TLS handshake error") ||
		!strings.Contains(out, "level=WARN") || !strings.Contains(out, "source=net/http") {
		t.Fatalf("server log:\n%s", out)
	}
}
