package cran

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// A .tar.xz source package is admitted, and one cut by its index and
// footer refused, the same under the dictionary budget as without it.
func TestUnitXZAnswersAreTheSameUnderTheBudget(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	const description = "Package: demo\nVersion: 1.0\nDescription: xz source\n"
	if err := tw.WriteHeader(&tar.Header{
		Name: "demo/DESCRIPTION", Mode: 0o644, Size: int64(len(description)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(description)); err != nil || tw.Close() != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	w, err := xz.NewWriter(&compressed)
	if err == nil {
		_, err = w.Write(archive.Bytes())
	}
	if err != nil || w.Close() != nil {
		t.Fatal(err)
	}
	whole := compressed.Bytes()
	footer := whole[len(whole)-12:]
	cut := whole[:len(whole)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
	for name, data := range map[string][]byte{"package": whole, "cut package": cut} {
		sameAnswersUnderTheBudget(t, name, func() (any, error) {
			return ReadPackage(
				t.Context(),
				bytes.NewReader(data),
				int64(len(data)),
				"src/contrib/demo_1.0.tar.xz",
			)
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
