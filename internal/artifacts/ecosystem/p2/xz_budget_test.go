package p2

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

// Every .xml.xz of the tests is admitted or refused the same under the
// dictionary budget as without it, wherever p2 reads one: whole, cut by
// its index and footer, and one whose header names 1.5 GiB, which
// MaxMetadata lowers.
func TestUnitXZAnswersAreTheSameUnderTheBudget(t *testing.T) {
	composite := []byte(
		`<repository type="org.eclipse.equinox.internal.p2.metadata.repository.CompositeMetadataRepository">` +
			`<children size="1"><child location="child"/></children></repository>`,
	)
	artifacts := p2TestXZ(t, artifactsXML)
	compositeXZ := p2TestXZ(t, composite)
	keep := func(raw string) (string, error) { return raw, nil }
	for name, body := range map[string][]byte{
		"artifacts.xml.xz":          artifacts,
		"cut artifacts.xml.xz":      xzCut(t, artifacts),
		"artifacts.xml.xz, 1.5 GiB": storedXZ(artifactsXML, 37),
	} {
		sameAnswersUnderTheBudget(t, "Validate "+name, func() (any, error) {
			return nil, Validate(
				t.Context(),
				"artifacts.xml.xz",
				bytes.NewReader(body),
				int64(len(body)),
			)
		})
		sameAnswersUnderTheBudget(t, "ArtifactChecksums "+name, func() (any, error) {
			return ArtifactChecksums(
				t.Context(),
				"artifacts.xml.xz",
				body,
				"plugins/plugin_1.0.0.jar",
			)
		})
	}
	for name, body := range map[string][]byte{
		"compositeContent.xml.xz":     compositeXZ,
		"cut compositeContent.xml.xz": xzCut(t, compositeXZ),
	} {
		sameAnswersUnderTheBudget(t, "EditComposite "+name, func() (any, error) {
			return EditComposite(t.Context(), "compositeContent.xml.xz", body, keep)
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
