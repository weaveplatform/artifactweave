package nix

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

// Every xz NAR and channel tarball of the tests is admitted or refused the
// same under the dictionary budget as without it: the fixture NAR whole
// and cut by its index and footer, a stored NAR, and a channel tarball
// whole and cut.
func TestUnitXZAnswersAreTheSameUnderTheBudget(t *testing.T) {
	_, archive, _ := fixture(t)
	stored := storedXZ(regular("hello\n"), 20)
	tarball := compressXZ(t, channelTar(t, 0))
	for name, data := range map[string][]byte{
		"NAR":        archive,
		"cut NAR":    archive[:len(archive)-xzTail(t, archive)],
		"stored NAR": stored,
	} {
		sameAnswersUnderTheBudget(t, name, func() (any, error) {
			return ReadArchive(t.Context(), bytes.NewReader(data), int64(len(data)), "xz")
		})
	}
	for name, data := range map[string][]byte{
		"channel":     tarball,
		"cut channel": tarball[:len(tarball)-xzTail(t, tarball)],
	} {
		sameAnswersUnderTheBudget(t, name, func() (any, error) {
			return nil, ReadChannel(t.Context(), bytes.NewReader(data), int64(len(data)))
		})
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
