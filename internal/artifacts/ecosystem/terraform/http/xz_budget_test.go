package terraformhttp

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// A remote .tar.xz and .xz module is admitted, and one cut by its index
// and footer refused, the same under the dictionary budget as without it.
func TestUnitXZAnswersAreTheSameUnderTheBudget(t *testing.T) {
	for name, whole := range map[string][]byte{
		"module.tar.xz": archiveXZ(t, archiveTar(t, "main.tf")),
		"module.xz":     archiveXZ(t, []byte("module bytes")),
	} {
		for label, data := range map[string][]byte{name: whole, "cut " + name: xzCut(t, whole)} {
			sameAnswersUnderTheBudget(t, label, func() (any, error) {
				return nil, validateCompleteArchive(
					t.Context(), bytes.NewReader(data), int64(len(data)), name,
					remoteArchiveEntries, remoteArchiveInflated,
				)
			})
		}
	}
}

// sameAnswersUnderTheBudget runs answer with the xz dictionary budget off,
// then with a budget of one byte that an open reader holds, so each decode
// waits for that reader and then goes in alone, every dictionary being
// larger than the budget. The budget changes no answer (#1354): both runs
// must give the same value and the same error.
func sameAnswersUnderTheBudget(t *testing.T, name string, answer func() (any, error)) {
	t.Helper()
	defer xzfile.SetBudget(xzfile.DefaultBudget)
	xzfile.SetBudget(0)
	want, wantErr := answer()
	xzfile.SetBudget(1)
	var held bytes.Buffer
	w, err := xz.WriterConfig{DictCap: lzma.MinDictCap}.NewWriter(&held)
	if err == nil {
		_, err = w.Write([]byte("held"))
	}
	if err != nil || w.Close() != nil {
		t.Fatalf("holder file: %v", err)
	}
	hold, err := xzfile.NewReader(t.Context(), &held, 0)
	if err == nil {
		_, err = io.ReadFull(hold, make([]byte, 1))
	}
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	type answered struct {
		got any
		err error
	}
	done := make(chan answered, 1)
	go func() {
		got, err := answer()
		done <- answered{got, err}
	}()
	select {
	case <-done:
		_ = hold.Close()
		t.Fatalf("%s decoded while another reader held the whole budget", name)
	case <-time.After(50 * time.Millisecond):
	}
	_ = hold.Close()
	got := <-done
	if !reflect.DeepEqual(got.got, want) || fmt.Sprint(got.err) != fmt.Sprint(wantErr) {
		t.Errorf(
			"%s: %+v, %v under the budget; %+v, %v without it",
			name,
			got.got,
			got.err,
			want,
			wantErr,
		)
	}
}
