package npm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

func TestUnitRegistryKeysKeepFirstWholeEntry(t *testing.T) {
	parts := []virtual.Part[[]json.RawMessage]{
		{Doc: []json.RawMessage{json.RawMessage(`{"keyid":"same","key":"first","expires":null,"future":[1]}`), json.RawMessage(`{"keyid":"other","key":"second"}`)}},
		{
			Doc: []json.RawMessage{
				json.RawMessage(`{"keyid":"\u0073ame","key":"loser","expires":"2000-01-01"}`),
				json.RawMessage(`{"keyid":"later","key":"third"}`),
				json.RawMessage(`{"opaque":true}`),
				json.RawMessage(`{"keyid":3,"key":"opaque-id"}`),
			},
		},
	}
	got, err := (KeysMerger{}).Merge(t.Context(), parts)
	if err != nil {
		t.Fatal(err)
	}
	want := []json.RawMessage{parts[0].Doc[0], parts[0].Doc[1], parts[1].Doc[1], parts[1].Doc[2], parts[1].Doc[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %s want %s", got, want)
	}
	got[0][0] = 'x'
	if parts[0].Doc[0][0] != '{' {
		t.Fatal("merged result mutated its supplying member")
	}
}

func TestUnitRegistryKeysEmptyInvalidAndCancelled(t *testing.T) {
	got, err := (KeysMerger{}).Merge(t.Context(), nil)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	for _, raw := range []string{`null`, `[]`, `bad`} {
		_, err := (KeysMerger{}).Merge(t.Context(), []virtual.Part[[]json.RawMessage]{{Doc: []json.RawMessage{json.RawMessage(raw)}}})
		if !errors.Is(err, ErrInvalidKeyEntry) {
			t.Fatal(raw, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (KeysMerger{}).Merge(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
