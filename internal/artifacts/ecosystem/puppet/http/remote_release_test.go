package puppethttp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// recordStub answers a remote's read of one release's upstream record,
// v3/releases/<slug>, from the address the remote asked for, as the source
// boundary does. Without at, the upstream answers 404, so a remote whose
// test sets no record answers a release its pages leave out as it did
// before #1344. The other fields change the answer as metadataSource's
// change a release page's.
type recordStub struct {
	at              func(source.Resource) (body string, missing bool)
	base            string
	readError       error
	noRevalidate    bool
	revalidateError error
	noStream        bool
	noAuthority     bool
	check           func(context.Context, string) error
	skipValidation  bool
}

func (s recordStub) read(r *http.Request, resource source.Resource) (source.Result, error) {
	if s.readError != nil {
		return source.Result{}, s.readError
	}
	if s.at == nil {
		return source.Result{}, kernel.ErrNotFound
	}
	body, missing := s.at(resource)
	if missing {
		return source.Result{}, kernel.ErrNotFound
	}
	at, err := url.Parse(resource.ExternalURL)
	if err != nil {
		return source.Result{}, err
	}
	at.RawQuery = ""
	check := s.check
	if check == nil {
		check = func(context.Context, string) error { return nil }
	}
	result := source.Result{
		Stream: &source.Stream{
			Body: io.NopCloser(strings.NewReader(body)), Status: 200, Size: int64(len(body)),
		},
		Metadata:        source.MetadataResponse{BaseURL: at.String()},
		CheckRead:       check,
		CheckPermission: check,
	}
	if s.base != "" {
		result.Metadata.BaseURL = s.base
	}
	if !s.noRevalidate {
		result.Revalidate = func(context.Context) error { return s.revalidateError }
	}
	if s.noStream {
		result.Stream = nil
	}
	if s.noAuthority {
		result.CheckRead, result.CheckPermission = nil, nil
	}
	if !s.skipValidation {
		if err := resource.ValidateMetadata(
			r.Context(), strings.NewReader(body), int64(len(body)), result.Metadata,
		); err != nil {
			return source.Result{}, err
		}
	}
	return result, nil
}

// releaseBody is an upstream's record of acme-widget at version, as
// upstreamRelease gives it, with extra's fields.
func releaseBody(t *testing.T, version string, extra map[string]any) string {
	t.Helper()
	data, err := json.Marshal(upstreamRelease(t, version, extra))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// served returns a recordStub tune that serves body for every release
// record read, and records each read in reads.
func served(body string, reads *[]source.Resource) func(*metadataSource) {
	return func(s *metadataSource) {
		s.release.at = func(r source.Resource) (string, bool) {
			if reads != nil {
				*reads = append(*reads, r)
			}
			return body, false
		}
	}
}

// A remote answers a release its upstream's release pages leave out from
// the upstream's own record of that release, as RTFACT-19130 shows a 6.8.6
// remote answering a Puppet Enterprise module's release (#1344) [I]. The
// record is asked for without the fields no record answers, under the
// release pages' authority, and only for a release the pages leave out: a
// listed release answers from the pages as before, and the list still
// lists only what the pages hold.
func TestUnitRemoteReleaseTheListLeavesOut(t *testing.T) {
	var reads []source.Resource
	f := remoteFixture(t, remotePageBody(t, upstreamRelease(t, "1.0.0", nil)),
		served(releaseBody(t, "2.0.0", map[string]any{"tags": []string{"pe"}}), &reads))
	wantBody(t, f.do("GET", base+"/v3/releases/acme-widget-2.0.0", "admin"),
		remoteRecordJSON("2.0.0", linkedMetadata("2.0.0"), `["pe"]`)+"\n")
	if r := f.do("HEAD", base+"/v3/releases/acme-widget-2.0.0", "admin"); r.Code != 200 ||
		r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
	}
	if len(reads) != 2 {
		t.Fatalf("record reads: %d", len(reads))
	}
	got := reads[0]
	fields := []any{
		got.ExternalURL, got.Path, got.Variant, got.Kind, got.Method, got.PermissionFolder,
		got.PermissionPath, got.NoCrossOriginAuth, got.PolicyPath, got.BlackoutOnly,
		got.ExpectedContentType,
	}
	want := []any{
		"https://example.com/puppet/v3/releases/acme-widget-2.0.0?exclude_fields=reference+tasks+" +
			"plans+pdk+validation_score+malware_scan+deleted_for",
		"v3/releases/acme-widget-2.0.0", "puppet-forge-v3-release-v1", source.ResourceKindMetadata,
		http.MethodGet, true, "acme-widget", true, ".puppet/acme-widget/releases.json", false,
		"application/json",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("record read: %+v", got)
	}
	reads = nil
	wantBody(t, f.do("GET", base+"/v3/releases/acme-widget-1.0.0", "admin"),
		remoteRecordJSON("1.0.0", linkedMetadata("1.0.0"), `[]`)+"\n")
	wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget", "admin"),
		listJSON("/v3/releases?module=acme-widget", 20, 0, 1,
			remoteRecordJSON("1.0.0", linkedMetadata("1.0.0"), `[]`)))
	if len(reads) != 0 {
		t.Fatalf("a listed release read its record: %+v", reads)
	}
}

// A release neither the pages nor the upstream's record holds, or that the
// source denies the caller, answers 404 "Puppet release not found" as it
// did; the source reports the upstream's 404 in both of its forms.
func TestUnitRemoteReleaseTheUpstreamDoesNotHold(t *testing.T) {
	for name, tune := range map[string]func(*metadataSource){
		"no record": nil,
		"missing": func(s *metadataSource) {
			s.release.at = func(source.Resource) (string, bool) { return "", true }
		},
		"a 404 answer": func(s *metadataSource) {
			s.release.readError = apierror.New(404, "Not Found")
		},
		"denied": func(s *metadataSource) {
			served(releaseBody(t, "2.0.0", nil), nil)(s)
			s.release.check = func(_ context.Context, path string) error {
				if path == "acme-widget/acme-widget-2.0.0.tar.gz" {
					return source.ErrDenied
				}
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := remoteFixture(t, remotePageBody(t, upstreamRelease(t, "1.0.0", nil)), tune)
			wantError(t, f.do("GET", base+"/v3/releases/acme-widget-2.0.0", "admin"), 404,
				"Puppet release not found")
		})
	}
}

// An upstream record that names another release or module, is not one
// valid release, is larger than any metadata a remote reads, or was read
// from anywhere but the release's record fails as a release page that
// holds it would, and the read's own failures answer as a release page's
// do; the download policy's refusal keeps its own answer.
func TestUnitRemoteReleaseRecordFailures(t *testing.T) {
	good := releaseBody(t, "2.0.0", nil)
	record := func(body string) func(*metadataSource) { return served(body, nil) }
	with := func(tune func(*recordStub)) func(*metadataSource) {
		return func(s *metadataSource) {
			served(good, nil)(s)
			tune(&s.release)
		}
	}
	const invalid = "invalid upstream Puppet metadata"
	for _, tc := range []struct {
		name    string
		tune    func(*metadataSource)
		status  int
		message string
	}{
		{"another version", record(releaseBody(t, "2.0.1", nil)), 502, invalid},
		{"another module", record(releaseBody(t, "2.0.0", map[string]any{
			"metadata": json.RawMessage(strings.ReplaceAll(linkedManifest("2.0.0"),
				`"acme-widget"`, `"acme-other"`)),
		})), 502, invalid},
		{"no metadata", record(releaseBody(t, "2.0.0", map[string]any{"metadata": nil})), 502, invalid},
		{"bad checksum", record(releaseBody(t, "2.0.0", map[string]any{
			"file_sha256": strings.Repeat("z", 64),
		})), 502, invalid},
		{"archive elsewhere", record(releaseBody(t, "2.0.0", map[string]any{
			"file_uri": "https://elsewhere.example/acme-widget-2.0.0.tar.gz",
		})), 502, invalid},
		{"archive outside the Forge", record(releaseBody(t, "2.0.0", map[string]any{
			"file_uri": "/other/acme-widget-2.0.0.tar.gz",
		})), 502, invalid},
		{"not an object", record(`[1]`), 502, invalid},
		{"empty", record(``), 502, invalid},
		{"too large", record(`{"version":"2.0.0","x":"` + strings.Repeat("x", 16<<20) + `"}`), 502, invalid},
		{"another record", with(func(s *recordStub) {
			s.base = "https://example.com/puppet/v3/releases/acme-widget-2.0.1"
		}), 502, invalid},
		{"outside the registry", with(func(s *recordStub) {
			s.base = "https://elsewhere.example/puppet/v3/releases/acme-widget-2.0.0"
		}), 502, invalid},
		{"outside the registry, unvalidated", with(func(s *recordStub) {
			s.skipValidation = true
			s.base = "https://elsewhere.example/puppet/v3/releases/acme-widget-2.0.0"
		}), 502, invalid},
		{"another version, unvalidated", func(s *metadataSource) {
			record(releaseBody(t, "2.0.1", nil))(s)
			s.release.skipValidation = true
		}, 502, invalid},
		{"upstream 503", with(func(s *recordStub) {
			s.readError = &source.HTTPError{Status: 503}
		}), 503, "Service Unavailable"},
		{"no revalidation", with(func(s *recordStub) { s.noRevalidate = true }), 502, "Bad Gateway"},
		{"no authority", with(func(s *recordStub) { s.noAuthority = true }), 502, "Bad Gateway"},
		{
			"stale", with(func(s *recordStub) { s.revalidateError = source.ErrSuperseded }), 503,
			"Service Unavailable",
		},
		{"no body", with(func(s *recordStub) { s.noStream = true }), 502, invalid},
		{"authority failure", with(func(s *recordStub) {
			s.check = func(context.Context, string) error { return source.ErrUnavailable }
		}), 502, "Bad Gateway"},
		// The download policy's 404 is the remote's own refusal, not the
		// upstream's missing record.
		{"refused", with(func(s *recordStub) {
			s.readError = format.Rejectf(404, "refused")
		}), 404, "refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := remoteFixture(t, remotePageBody(t, upstreamRelease(t, "1.0.0", nil)), tc.tune)
			wantError(t, f.do("GET", base+"/v3/releases/acme-widget-2.0.0", "admin"),
				tc.status, tc.message)
		})
	}
}
