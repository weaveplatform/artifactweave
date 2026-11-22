package peerstorage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type cancelRead struct {
	io.Reader
	cancel context.CancelFunc
}

func (r cancelRead) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}

func TestUnitPullMetadataCancellationCannotBecomeAnEmptySuccessfulDocument(t *testing.T) {
	for _, mode := range []string{"before document", "during document", "listing fields", "listing entries"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var err error
			switch mode {
			case "before document":
				cancel()
				err = decodeDocument(ctx, strings.NewReader(`{}`), new(map[string]any))
			case "during document":
				err = decodeDocument(ctx, cancelRead{Reader: strings.NewReader(`{}`), cancel: cancel}, new(map[string]any))
			case "listing fields":
				_, err = (&Reader{}).list(ctx, cancelRead{Reader: strings.NewReader(`{"files":[]}`), cancel: cancel}, "", 2, "")
			case "listing entries":
				cancel()
				_, err = (&Reader{}).listEntries(ctx, json.NewDecoder(strings.NewReader(`[{"uri":"/a","folder":false}]`)), "", 2, "")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation became complete metadata", err)
			}
		})
	}
}
