package debian

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"

	"github.com/weaveplatform/artifactweave/internal/platform/xzfile"
)

// Every xz package and index of the tests is admitted or refused the same
// under the dictionary budget as without it: a package whose control and
// data archives are xz, whole and with either cut by its index and
// footer, and a Packages, installer Packages and Sources index, whole and
// cut. A package reads its two xz archives one after the other.
func TestUnitXZAnswersAreTheSameUnderTheBudget(t *testing.T) {
	control := compressed(t, "xz", makeTar(t, "./control", validControl))
	data := compressed(t, "xz", makeTar(t, "./usr/bin/hello", "hello"))
	deb := func(control, data []byte) []byte {
		return makeAR(t,
			fixtureMember{"debian-binary", []byte("2.0\n")},
			fixtureMember{"control.tar.xz", control},
			fixtureMember{"data.tar.xz", data},
		)
	}
	for name, body := range map[string][]byte{
		"package":            deb(control, data),
		"cut control.tar.xz": deb(xzCut(t, control), data),
		"cut data.tar.xz":    deb(control, xzCut(t, data)),
	} {
		sameAnswersUnderTheBudget(t, name, func() (any, error) {
			return ReadBinary(t.Context(), bytes.NewReader(body), int64(len(body)))
		})
	}
	packages := "Package: alpha\nVersion: 1.0\nArchitecture: amd64\n" +
		"Filename: pool/alpha.deb\nSize: 4\nSHA256: " + strings.Repeat("a", 64) + "\n\n"
	sources, err := Sources([]IndexedSource{dpkgSourceIndexEntry(t)})
	if err != nil {
		t.Fatal(err)
	}
	packagesXZ := compressed(t, "xz", []byte(packages))
	installerXZ := compressed(t, "xz", []byte(strings.Replace(packages, ".deb", ".udeb", 1)))
	sourcesXZ := compressed(t, "xz", sources)
	for _, body := range [][]byte{packagesXZ, xzCut(t, packagesXZ)} {
		sameAnswersUnderTheBudget(t, "Packages", func() (any, error) {
			return DecodePackagesIndex(t.Context(), body)
		})
	}
	for _, body := range [][]byte{installerXZ, xzCut(t, installerXZ)} {
		sameAnswersUnderTheBudget(t, "installer Packages", func() (any, error) {
			return DecodeInstallerPackagesIndex(t.Context(), body)
		})
	}
	for _, body := range [][]byte{sourcesXZ, xzCut(t, sourcesXZ)} {
		sameAnswersUnderTheBudget(t, "Sources", func() (any, error) {
			return DecodeSourcesIndex(t.Context(), body)
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
