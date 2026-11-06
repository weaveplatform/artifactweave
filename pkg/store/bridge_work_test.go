package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The bridge must alias the work queue's port, not declare its own (decision
// 0044, "Type-alias bridges"): the SQL and in-memory stores implement it
// under the store names while the queue, and the blob mirror once its leases
// move, use the work names, and neither side converts. A declared LeaseStore
// would still compile everywhere, because interfaces with the same methods
// are assignable both ways. A struct is assignable only to its own type and a
// pointer only to a pointer to the identical type, so these lines compile
// only while each pair is one type.
var (
	_ work.WorkLease   = store.WorkLease{}
	_ work.WorkFailure = store.WorkFailure{}
	_ *work.WorkQueue  = (*store.WorkQueue)(nil)
	_ *work.LeaseStore = (*store.LeaseStore)(nil)
)

// A constant has no identity beyond its type and value, so the bridged limits
// are compared on both. The server answers 400 to a reindex or snapshot key
// longer than store.MaxWorkKey before it enqueues, while ValidateWork refuses
// with work.MaxWorkKey: a copy that drifted would turn that 400 into a failed
// enqueue.
func TestUnitBridgeWorkLimits(t *testing.T) {
	for _, c := range []struct {
		name           string
		legacy, target any
	}{
		{"MaxWorkKind", store.MaxWorkKind, work.MaxWorkKind},
		{"MaxWorkKey", store.MaxWorkKey, work.MaxWorkKey},
		{"MaxWorkPayload", store.MaxWorkPayload, work.MaxWorkPayload},
	} {
		if c.legacy != c.target {
			t.Errorf("store.%[1]s is %#[2]v (%[2]T), work.%[1]s is %#[3]v (%[3]T)",
				c.name, c.legacy, c.target)
		}
	}
}

// The SQL and in-memory queues call these through the store names until
// STO-12 moves them, so a copy of either function would leave two sets of
// rules that could drift apart unnoticed. Both names must hold the work
// function, and what it answers must match ErrInvalid under the store and
// kernel names with the same text.
func TestUnitBridgeWorkValidators(t *testing.T) {
	same := func(name string, legacy, target any) {
		t.Helper()
		if reflect.ValueOf(legacy).Pointer() != reflect.ValueOf(target).Pointer() {
			t.Errorf("store.%s is not work.%[1]s", name)
		}
	}
	same("ValidateWork", store.ValidateWork, work.ValidateWork)
	same("ValidateLease", store.ValidateLease, work.ValidateLease)

	answers := func(what string, legacy, target error, ok bool) {
		t.Helper()
		if ok {
			if legacy != nil || target != nil {
				t.Errorf("%s: refused: %v / %v", what, legacy, target)
			}
			return
		}
		for _, err := range []error{legacy, target} {
			if !errors.Is(err, store.ErrInvalid) || !errors.Is(err, kernel.ErrInvalid) {
				t.Errorf("%s: %v does not match ErrInvalid under both names", what, err)
			}
		}
		if legacy != nil && target != nil && legacy.Error() != target.Error() {
			t.Errorf("%s: %q under the store name, %q under the work name", what, legacy, target)
		}
	}
	// A JSON string one byte over the limit with its quotes.
	large := []byte(`"` + strings.Repeat("x", work.MaxWorkPayload-1) + `"`)
	for _, c := range []struct {
		what, kind, key string
		payload         []byte
		ok              bool
	}{
		{"a valid item", "maven", "k", []byte(`{"a":1}`), true},
		{"no payload", "maven", "k", nil, true},
		{"no kind", "", "k", nil, false},
		{"a long kind", strings.Repeat("k", work.MaxWorkKind+1), "k", nil, false},
		{"a long key", "maven", strings.Repeat("k", work.MaxWorkKey+1), nil, false},
		{"a large payload", "maven", "k", large, false},
		{"a payload that is not JSON", "maven", "k", []byte("{"), false},
	} {
		answers(c.what, store.ValidateWork(c.kind, c.key, c.payload),
			work.ValidateWork(c.kind, c.key, c.payload), c.ok)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		what string
		req  work.WorkLease
		ok   bool
	}{
		{"a valid lease", work.WorkLease{Holder: "h", Until: now.Add(time.Minute), Now: now}, true},
		{"no holder", work.WorkLease{Until: now.Add(time.Minute), Now: now}, false},
		{"no expiry", work.WorkLease{Holder: "h", Now: now}, false},
		{"an expiry at now", work.WorkLease{Holder: "h", Until: now, Now: now}, false},
	} {
		answers(c.what, store.ValidateLease(c.req), work.ValidateLease(c.req), c.ok)
	}
}
