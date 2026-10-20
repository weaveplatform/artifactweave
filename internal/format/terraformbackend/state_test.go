package terraformbackend_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	tfbe "github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
)

func keys(t *testing.T, fill byte) *crypt.Keyring {
	t.Helper()
	k, err := crypt.New(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

const aState = `{"version":4,"serial":7,"lineage":"6a3b","outputs":{},"resources":[]}`

func TestParse(t *testing.T) {
	serial, lineage, err := tfbe.Parse([]byte(aState))
	if err != nil || serial != 7 || lineage != "6a3b" {
		t.Fatalf("Parse = %d, %q, %v", serial, lineage, err)
	}
	for _, bad := range []string{
		"", "not json", "[1]", `{"lineage":"x"}`, `{"serial":-1}`, `{"serial":1.5}`,
		`{"serial":"x"}`, `{"serial":1} trailing`,
	} {
		if _, _, err := tfbe.Parse([]byte(bad)); !errors.Is(err, tfbe.ErrNotState) {
			t.Errorf("Parse(%q) = %v", bad, err)
		}
	}
}

func TestSealAndOpen(t *testing.T) {
	k := keys(t, 'a')
	sealed, err := tfbe.Seal(k, "tf", "app", []byte(aState))
	if err != nil || !crypt.IsSealed(sealed) || bytes.Contains(sealed, []byte("6a3b")) {
		t.Fatalf("Seal = %q, %v", sealed, err)
	}
	again, err := tfbe.Seal(k, "tf", "app", []byte(aState))
	if err != nil || bytes.Equal(again, sealed) {
		t.Fatal("two seals of one state are equal: the checksum would reveal the content")
	}
	if plain, err := tfbe.Open(k, "tf", "app", sealed); err != nil || string(plain) != aState {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	// A plain state opens as it is, with a key or without.
	if plain, err := tfbe.Open(
		nil,
		"tf",
		"app",
		[]byte(aState),
	); err != nil ||
		string(plain) != aState {
		t.Fatalf("Open of a plain state = %q, %v", plain, err)
	}
	// The row ties a sealed state to its workspace and repository.
	for _, where := range [][2]string{{"tf", "other"}, {"other", "app"}} {
		if _, err := tfbe.Open(k, where[0], where[1], sealed); !errors.Is(err, crypt.ErrTampered) {
			t.Errorf("Open in %s/%s = %v", where[0], where[1], err)
		}
	}
	if _, err := tfbe.Open(keys(t, 'b'), "tf", "app", sealed); !errors.Is(err, crypt.ErrWrongKey) {
		t.Errorf("Open under another master key = %v", err)
	}
	if _, err := tfbe.Open(nil, "tf", "app", sealed); !errors.Is(err, tfbe.ErrNoKeyring) {
		t.Errorf("Open without a key = %v", err)
	}
	if _, err := tfbe.Seal(nil, "tf", "app", []byte(aState)); !errors.Is(err, tfbe.ErrNoKeyring) {
		t.Errorf("Seal without a key = %v", err)
	}
}

func TestMeta(t *testing.T) {
	k := keys(t, 'a')
	sealed, err := tfbe.Seal(k, "tf", "app", []byte(aState))
	if err != nil {
		t.Fatal(err)
	}
	want := tfbe.Meta{
		Serial: 7, Lineage: "6a3b", MD5: tfbe.MD5([]byte(aState)), Size: int64(len(aState)),
		Stored: tfbe.SHA256(sealed), Sealed: true,
	}
	if got := tfbe.MetaOf(k, "tf", "app", sealed); got != want {
		t.Fatalf("MetaOf(sealed) = %+v; want %+v", got, want)
	}
	if got, ok := tfbe.DecodeMeta(want.Encode()); !ok || got != want {
		t.Fatalf("DecodeMeta(Encode) = %+v, %v", got, ok)
	}
	for _, bad := range []string{"", "{}", "not json"} {
		if _, ok := tfbe.DecodeMeta([]byte(bad)); ok {
			t.Errorf("DecodeMeta(%q) succeeded", bad)
		}
	}
	plain := tfbe.MetaOf(nil, "tf", "app", []byte(aState))
	if plain.Sealed || plain.Unreadable || plain.Serial != 7 ||
		plain.Stored != tfbe.SHA256([]byte(aState)) {
		t.Fatalf("MetaOf(plain) = %+v", plain)
	}
	for name, m := range map[string]tfbe.Meta{
		"without a key":  tfbe.MetaOf(nil, "tf", "app", sealed),
		"in another row": tfbe.MetaOf(k, "tf", "other", sealed),
		"no state":       tfbe.MetaOf(k, "tf", "app", []byte("<html/>")),
	} {
		if !m.Unreadable || m.Serial != 0 || m.MD5 != "" || m.Stored == "" {
			t.Errorf("MetaOf %s = %+v", name, m)
		}
	}
	if m := tfbe.MetaOf(nil, "tf", "app", sealed); m.Size != int64(len(sealed)) || !m.Sealed {
		t.Errorf("an unreadable sealed state = %+v", m)
	}
}

func TestReadStored(t *testing.T) {
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, err := blobs.BeginUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Append(ctx, bytes.NewReader([]byte(aState))); err != nil {
		t.Fatal(err)
	}
	info, err := u.Commit(ctx, blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tfbe.ReadStored(
		ctx,
		blobs,
		info.Digests.SHA256,
	); err != nil ||
		string(got) != aState {
		t.Fatalf("ReadStored = %q, %v", got, err)
	}
	if _, err := tfbe.ReadStored(ctx, blobs, tfbe.SHA256([]byte("absent"))); err == nil {
		t.Fatal("ReadStored of absent content succeeded")
	}
}
