package bower

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitBowerDependencyArchiveRejectsCorruptionAndAmbiguity(t *testing.T) {
	manifest := archiveEntry{"dep/bower.json", `{"name":"dep"}`, tar.TypeReg}
	valid := bowerArchive(t, []archiveEntry{manifest})
	badCRC := bytes.Clone(valid)
	badCRC[len(badCRC)-8] ^= 1
	tooMany := make([]archiveEntry, maxArchiveFiles+1)
	for i := range tooMany {
		tooMany[i] = archiveEntry{"dep/", "", tar.TypeDir}
	}
	tooMany[0] = manifest
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"gzip checksum", badCRC},
		{"gzip trailer truncated", valid[:len(valid)-4]},
		{"duplicate path", bowerArchive(t, []archiveEntry{manifest, manifest})},
		{"path traversal", bowerArchive(t, []archiveEntry{manifest, {"../escape", "secret", tar.TypeReg}})},
		{"directory traversal", bowerArchive(t, []archiveEntry{manifest, {"../", "", tar.TypeDir}})},
		{"file outside provider root", bowerArchive(t, []archiveEntry{manifest, {"outside.js", "secret", tar.TypeReg}})},
		{"directory count bound", bowerArchive(t, tooMany)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if files, err := dependencyFiles(tc.data, "dep"); err == nil || files != nil {
				t.Fatalf("unsafe archive admitted: files=%v err=%v", files, err)
			}
		})
	}
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	if _, err := gz.Write([]byte("incomplete tar header")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if files, err := dependencyFiles(output.Bytes(), "dep"); err == nil || files != nil {
		t.Fatalf("truncated tar admitted: %v %v", files, err)
	}
}

func TestUnitBowerDependencyZIPChecksContentAndCompression(t *testing.T) {
	for _, method := range []uint16{zip.Store, 99} {
		t.Run(fmt.Sprint(method), func(t *testing.T) {
			var out bytes.Buffer
			zw := zip.NewWriter(&out)
			data := []byte(`{"name":"dep"}`)
			// An incorrect CRC must be rejected even when the manifest is valid.
			w, err := zw.CreateRaw(&zip.FileHeader{
				Name: "bower.json", Method: method, CRC32: 1,
				CompressedSize64: uint64(len(data)), UncompressedSize64: uint64(len(data)),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			if files, err := dependencyFiles(out.Bytes(), "dep"); err == nil || files != nil {
				t.Fatalf("corrupt ZIP admitted: %v %v", files, err)
			}
		})
	}
}

func TestAccBowerDependencyReadFailuresNeverDeliverOrRecordUsage(t *testing.T) {
	for _, fault := range []string{"empty result", "missing blob", "interrupted stream", "corrupt archive", "revoked source"} {
		t.Run(fault, func(t *testing.T) {
			f, base := dependencyFixture(t)
			used, closed, reads := false, false, 0
			f.sources.HTTP = bowerSourceRead(
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					reads++
					result := source.Result{
						Used: func(context.Context) error { used = true; return nil },
					}
					switch fault {
					case "empty result":
					case "missing blob":
						result.Item = &types.Item{
							RepoKey: q.Repo.Key,
							Cached:  true,
							Name:    q.Resource.Path,
							Actual:  types.Checksums{SHA256: strings.Repeat("a", 64)},
						}
					default:
						var reader io.Reader = bytes.NewReader(bowerArchive(t, []archiveEntry{{"dep/bower.json", `{"name":"dep"}`, 0}}))
						if fault == "interrupted stream" {
							reader = brokenMetadataBody{}
						} else if fault == "corrupt archive" {
							reader = strings.NewReader("not an archive")
						}
						result.Stream = &source.Stream{
							Body:   &closingDependencyBody{Reader: reader, closed: &closed},
							Status: http.StatusOK,
						}
						if fault == "revoked source" {
							checks := 0
							result.Revalidate = func(context.Context) error {
								checks++
								if checks > 1 {
									return source.ErrDenied
								}
								return nil
							}
						}
					}
					return result, nil
				},
			)
			rec := f.get(
				base+"binaries/external/dep.git/"+url.PathEscape(
					dependencyTarget(f.up.URL+"/external/dep.tgz"),
				),
				"admin",
			)
			want := http.StatusBadGateway
			if fault == "missing blob" {
				want = http.StatusInternalServerError
			} else if fault == "revoked source" {
				want = http.StatusForbidden
			}
			if rec.Code != want || used || reads != 1 {
				t.Fatalf(
					"failed dependency: status=%d want=%d used=%t reads=%d: %s",
					rec.Code,
					want,
					used,
					reads,
					rec.Body,
				)
			}
			if fault != "empty result" && fault != "missing blob" && !closed {
				t.Fatal("failed dependency stream was not closed")
			}
		})
	}
}

type closingDependencyBody struct {
	io.Reader
	closed *bool
}

func (b *closingDependencyBody) Close() error { *b.closed = true; return nil }

type localArchiveFaultBlobs struct {
	blob.Store
	digest string
	fault  string
	cancel context.CancelFunc
}

func (b localArchiveFaultBlobs) Open(
	ctx context.Context,
	digest string,
) (io.ReadSeekCloser, blob.Info, error) {
	if digest != b.digest {
		return b.Store.Open(ctx, digest)
	}
	if b.fault == "missing blob" {
		return nil, blob.Info{}, blob.ErrNotFound
	}
	r, info, err := b.Store.Open(ctx, digest)
	if err != nil {
		return nil, info, err
	}
	if b.fault == "cancelled" {
		b.cancel()
	}
	if b.fault == "interrupted" {
		return interruptedArchiveReader{r}, info, nil
	}
	return r, info, nil
}

type interruptedArchiveReader struct{ io.ReadSeekCloser }

func (r interruptedArchiveReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestAccBowerLocalArchiveFailureDoesNotDeliverPartialBytes(t *testing.T) {
	for _, fault := range []string{"missing blob", "interrupted", "invalid size", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			f := newFixture(t)
			c := f.repo("local", repo.Local)
			f.deploy(c, "hello", "1.0.0", "bower.json", `{"name":"hello","version":"1.0.0"}`)
			f.deploy(c, "hello", "1.0.0", "index.js", "protected artifact bytes")
			f.deploy(c, "hello", "1.0.0", "last.js", "last artifact bytes")
			it, err := f.db.Items().Get(t.Context(), c.Key, false, "hello/1.0.0/index.js")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if fault == "invalid size" {
				it.Size = -1
				if _, err := f.db.Items().Put(t.Context(), it); err != nil {
					t.Fatal(err)
				}
			}
			f.items.Blobs = localArchiveFaultBlobs{
				Store:  f.items.Blobs,
				digest: it.Actual.SHA256,
				fault:  fault,
				cancel: cancel,
			}
			r := httptest.NewRequest(http.MethodGet, "/artifactory/api/bower/local/binaries/local/hello.git/v1.0.0", nil).
				WithContext(ctx)
			r.Header.Set("X-User", "admin")
			out := httptest.NewRecorder()
			f.router.ServeHTTP(out, r)
			if out.Code != http.StatusInternalServerError ||
				bytes.HasPrefix(out.Body.Bytes(), []byte{0x1f, 0x8b}) {
				t.Fatalf("failed archive delivered: %d %s", out.Code, out.Body)
			}
		})
	}
}

func TestAccBowerDependencyAdmissionChecksLengthBeforeCaching(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size        int64
		interrupted bool
	}{
		{"empty", 0, false},
		{"oversized", maxArchiveBytes + 1, false},
		{"truncated", 1000, false},
		{"read failure", 1000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, base := dependencyFixture(t)
			checked := false
			valid := bowerArchive(
				t,
				[]archiveEntry{{"dep/bower.json", `{"name":"dep"}`, tar.TypeReg}},
			)
			f.sources.HTTP = bowerSourceRead(
				func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					checked = true
					if q.Resource.Validate == nil {
						t.Fatal("dependency admission validator is absent")
					}
					body := &admissionReader{
						ReadSeeker: bytes.NewReader(valid),
						fail:       tc.interrupted,
					}
					size := tc.size
					if tc.name == "truncated" {
						size = int64(len(valid) + 1)
					}
					if err := q.Resource.Validate(ctx, body, size); err == nil {
						t.Fatal("invalid dependency length accepted for caching")
					}
					if size <= 0 || size > maxArchiveBytes {
						if body.reads != 0 {
							t.Fatal("invalid declared size consumed the archive")
						}
					} else if body.reads == 0 {
						t.Fatal("valid declared size never reached archive verification")
					}
					return source.Result{}, source.ErrInvalidResult
				},
			)
			out := f.get(
				base+"binaries/external/dep.git/"+url.PathEscape(
					dependencyTarget(f.up.URL+"/external/dep.tgz"),
				),
				"admin",
			)
			if out.Code != http.StatusBadGateway || !checked {
				t.Fatalf("admission bypassed: %d checked=%t", out.Code, checked)
			}
		})
	}
}

type admissionReader struct {
	io.ReadSeeker
	fail  bool
	reads int
}

func (r *admissionReader) Read(p []byte) (int, error) {
	r.reads++
	if r.fail {
		return 0, io.ErrUnexpectedEOF
	}
	return r.ReadSeeker.Read(p)
}

func TestUnitBowerArchiveCaptureBoundsMemberResponse(t *testing.T) {
	capture := &archiveCapture{header: make(http.Header)}
	if n, err := capture.Write([]byte("first")); n != 5 || err != nil {
		t.Fatalf("initial member bytes: %d %v", n, err)
	}
	if n, err := capture.Write(
		make([]byte, maxArchiveBytes),
	); n != 0 ||
		!errors.Is(err, source.ErrInvalidResult) {
		t.Fatalf("oversized member response accepted: %d %v", n, err)
	}
	if capture.body.String() != "first" || !errors.Is(capture.err, source.ErrInvalidResult) {
		t.Fatal("capture retained oversized bytes or lost its failure")
	}
}
