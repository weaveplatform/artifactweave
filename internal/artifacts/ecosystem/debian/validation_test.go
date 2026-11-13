package debian

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestUnitReadBinaryWithoutMetadataValidation (#1374): with 7.x's
// debian.metadata.validation off, a control field with no value, and a
// missing Maintainer or Description, no longer make a binary malformed; an
// empty field, with its continuation lines, is left out of the stanza the
// index lists, a field whose value starts on a continuation line among
// them, so the server can still read its own index. The archive, a duplicate field and the Package, Version and
// Architecture are still checked. With validation on, the default, every
// one of these is refused, as before.
func TestUnitReadBinaryWithoutMetadataValidation(t *testing.T) {
	const head = "Package: hello\nVersion: 1.0-1\nArchitecture: amd64\n"
	for _, tc := range []struct {
		name, control string
		// stanza is the control data the index lists without validation;
		// empty means the binary is refused either way.
		stanza string
	}{
		{
			name:    "empty maintainer",
			control: head + "Maintainer:\nDescription: test binary\n",
			stanza:  head + "Description: test binary\n",
		},
		{
			name:    "empty field and its continuation",
			control: head + "Depends: \n \t\nMaintainer: T <t@example.com>\nDescription: test binary\n",
			stanza:  head + "Maintainer: T <t@example.com>\nDescription: test binary\n",
		},
		{
			name:    "no maintainer or description",
			control: head,
			stanza:  head,
		},
		{
			name:    "value on a continuation line",
			control: head + "Maintainer: T <t@example.com>\nDescription:\n test binary\n",
			stanza:  head + "Maintainer: T <t@example.com>\n",
		},
		{name: "empty package", control: "Package:\nVersion: 1.0-1\nArchitecture: amd64\n"},
		{name: "no version", control: "Package: hello\nArchitecture: amd64\n"},
		{name: "duplicate field", control: head + "Package: other\n"},
		{name: "orphan continuation", control: " hello\n" + head},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fixture(t, tc.control)
			if _, err := ReadBinary(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
			); !errors.Is(
				err,
				errInvalidBinary,
			) {
				t.Fatalf("with validation: %v, want errInvalidBinary", err)
			}
			got, err := readBinaryArchive(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
				false,
			)
			if tc.stanza == "" {
				if !errors.Is(err, errInvalidBinary) {
					t.Fatalf("without validation: %+v, %v; want errInvalidBinary", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("without validation: %v", err)
			}
			if got.Name != "hello" || got.Version != "1.0-1" || got.Architecture != "amd64" ||
				string(got.Control) != tc.stanza {
				t.Fatalf("without validation: %+v (control %q), want hello 1.0-1 amd64 %q",
					got, got.Control, tc.stanza)
			}
			// The stanza the index writes still reads back as a Packages
			// index, as apt, a virtual repository and a snapshot read it.
			index, err := Packages([]IndexedBinary{{
				Binary: got, Path: "pool/main/h/hello/hello_1.0-1_amd64.deb", Size: 1,
				Checksums: kernel.Checksums{
					SHA256: string(bytes.Repeat([]byte("a"), 64)),
					MD5:    string(bytes.Repeat([]byte("b"), 32)),
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if records, err := ParsePackagesIndex(index); err != nil || len(records) != 1 {
				t.Fatalf("index %q reads back as %+v, %v", index, records, err)
			}
		})
	}
	notAR := []byte("not a debian package")
	if _, err := readBinaryArchive(
		t.Context(), bytes.NewReader(notAR), int64(len(notAR)), false,
	); !errors.Is(err, errInvalidBinary) {
		t.Fatalf("an unreadable archive without validation: %v, want errInvalidBinary", err)
	}
}

// TestUnitHandlerMetadataValidationSetting (#1374): the handler marks a
// binary with an empty Maintainer malformed by default, and with
// DisableMetadataValidation gives it its deb.name and deb.version and a
// version in the package index, on deploy and on reindex alike. An
// unreadable archive is marked malformed either way.
func TestUnitHandlerMetadataValidationSetting(t *testing.T) {
	ctx := t.Context()
	empty := fixture(t, "Package: hello\nVersion: 1.0-1\nArchitecture: amd64\nMaintainer:\n"+
		"Description: test binary\n")
	for _, tc := range []struct {
		name    string
		handler Handler
		want    model.Properties
		indexed []string
	}{
		{
			name:    "on",
			handler: Handler{},
			want: model.Properties{
				PropIndexStatus: {"debian: invalid binary package: malformed or duplicate control field"},
			},
		},
		{
			name:    "off",
			handler: Handler{Settings: Settings{DisableMetadataValidation: true}},
			want:    model.Properties{PropName: {"hello"}, PropVersion: {"1.0-1"}},
			indexed: []string{"hello"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := *repo.Defaults(&repo.Config{Key: "deb-local", Class: repo.Local, PackageType: repo.Debian})
			st := inmem.New()
			if err := st.Repos().Create(ctx, c); err != nil {
				t.Fatal(err)
			}
			blobs := digestBlobs{}
			put := func(name string, body []byte) model.Item {
				t.Helper()
				it, err := st.Items().Put(ctx, model.Item{
					RepoKey: c.Key, Name: name, Size: int64(len(body)),
					Actual: kernel.Checksums{SHA256: blobs.add(body)},
				})
				if err != nil {
					t.Fatal(err)
				}
				return it
			}
			it := put("pool/main/h/hello/hello_1.0-1_amd64.deb", empty)
			bad := put("pool/main/b/bad/bad_1.0_amd64.deb", []byte("not a debian package"))
			env := format.Env{Store: st, Blobs: blobs, Enqueue: func(
				_ context.Context, _, _ string, _ []byte,
			) error {
				return nil
			}}
			check := func(stage string) {
				t.Helper()
				got, err := st.Items().Properties(ctx, it.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{PropName, PropVersion, PropIndexStatus} {
					if !slices.Equal(got[key], tc.want[key]) {
						t.Errorf("%s: %s = %q, want %q", stage, key, got[key], tc.want[key])
					}
				}
				marked, err := st.Items().Properties(ctx, bad.ID)
				if err != nil {
					t.Fatal(err)
				}
				if want := []string{"debian: invalid binary package: ar magic"}; !slices.Equal(
					marked[PropIndexStatus], want,
				) {
					t.Errorf("%s: unreadable archive %s = %q, want %q",
						stage, PropIndexStatus, marked[PropIndexStatus], want)
				}
				packages, _, err := st.Packages().List(ctx, c.Key, kernel.Page{})
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, p := range packages {
					names = append(names, p.Name)
				}
				if !slices.Equal(names, tc.indexed) {
					t.Errorf("%s: package index holds %q, want %q", stage, names, tc.indexed)
				}
			}
			for _, deployed := range []model.Item{it, bad} {
				if err := tc.handler.Deployed(ctx, env, c, deployed); err != nil {
					t.Fatalf("deploy of %s: %v", deployed.Name, err)
				}
			}
			check("deploy")
			if err := st.Items().SetProperties(ctx, it.ID,
				model.Properties{PropIndexStatus: {"stale"}}, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if err := tc.handler.Reindex(ctx, env, c); err != nil {
				t.Fatalf("reindex: %v", err)
			}
			check("reindex")
		})
	}
}
