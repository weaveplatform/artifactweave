package huggingface

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestUnitMigrationStreamingHonorsCancellationAndReadFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := migrationReader{ctx: ctx, reader: strings.NewReader("unread")}
	if _, err := reader.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled migration kept reading: %v", err)
	}
	reader = migrationReader{ctx: t.Context(), reader: failedJSONBody{}}
	if _, err := reader.Read(make([]byte, 8)); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("storage read failure ignored: %v", err)
	}
}
