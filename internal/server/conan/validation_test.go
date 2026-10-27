package conan

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const testMD5 = "5d41402abc4b2a76b9719d911017c592" // MD5 of "hello"; Conan's manifest wire digest.

func TestUnitConanCredentialExchangeBoundary(t *testing.T) {
	k := pkgroute.Mount(chi.NewRouter(), pkgroute.Options{}, Protocol(Options{}))
	h := handler{k: k}
	for _, tc := range []struct {
		name, method, path, principal, basicUser, secret string
		password                                         bool
		expected                                         int
	}{
		{"anonymous exchange", "GET", "authenticate", "", "", "", false, 401},
		{"anonymous check", "GET", "check_credentials", "", "", "", false, 401},
		{"missing basic credential", "GET", "authenticate", "alice", "", "", false, 401},
		{"wrong basic user", "GET", "authenticate", "alice", "other", "password", true, 401},
		{"empty basic password", "GET", "authenticate", "alice", "alice", "", true, 401},
		{"password without issuer", "GET", "authenticate", "alice", "alice", "password", true, 501},
		{"token principal with password", "GET", "authenticate", "alice", "alice", "password", false, 501},
		{"credential check", "GET", "check_credentials", "alice", "", "", false, 200},
		{"token echo", "GET", "authenticate", "alice", "alice", "eyJ.conan.token", false, 200},
		{"method check", "POST", "authenticate", "alice", "alice", "password", true, 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/v1/users/"+tc.path, nil)
			if tc.principal != "" {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{
					Name: tc.principal, Password: tc.password,
				}))
			}
			if tc.basicUser != "" {
				r.SetBasicAuth(tc.basicUser, tc.secret)
			}
			w := httptest.NewRecorder()
			if tc.path == "authenticate" {
				h.authenticate(w, r)
			} else {
				h.checkCredentials(w, r)
			}
			if w.Code != tc.expected {
				t.Fatalf(
					"%s: status %d, want %d: %s",
					tc.path,
					w.Code,
					tc.expected,
					w.Body.String(),
				)
			}
			if tc.expected == http.StatusOK {
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("Conan credential response was cacheable")
				}
				if tc.path == "authenticate" && w.Body.String() != tc.secret {
					t.Fatalf("Conan token exchange changed token value: %q", w.Body.String())
				}
			}
		})
	}
}

func TestUnitConanManifestValidation(t *testing.T) {
	good := fmt.Sprintf("1\nconanfile.py: %s\n", testMD5)
	entries, err := parseManifest([]byte(good))
	if err != nil || entries["conanfile.py"] != testMD5 {
		t.Fatalf("valid Conan manifest: %v %v", entries, err)
	}
	for _, data := range []string{
		"", "1", "not-a-timestamp\nconanfile.py: " + testMD5,
		"1\nconanfile.py: short", "1\nconanfile.py " + testMD5,
		"1\n../escape: " + testMD5,
		"1\nconanmanifest.txt: " + testMD5,
		"1\nconanfile.py: " + testMD5 + "\nconanfile.py: " + testMD5,
		"1\nconanfile.py: zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
	} {
		if _, err := parseManifest([]byte(data)); err == nil {
			t.Errorf("accepted invalid Conan manifest %q", data)
		}
	}
	for _, size := range []int64{-1, maxManifest + 1} {
		if err := validateRemoteManifest(
			t.Context(),
			bytes.NewReader([]byte(good)),
			size,
		); err == nil {
			t.Errorf("accepted remote manifest size %d", size)
		}
	}
	if err := validateRemoteManifest(
		t.Context(),
		bytes.NewReader([]byte(good)),
		int64(len(good)),
	); err != nil {
		t.Fatal(err)
	}
	if err := validateRemoteManifest(
		t.Context(),
		strings.NewReader("1\n../escape: "+testMD5),
		64,
	); err == nil {
		t.Error("accepted invalid remote manifest")
	}
	if err := validateRemoteManifest(
		t.Context(),
		strings.NewReader(strings.Repeat("x", maxManifest+1)),
		maxManifest,
	); err == nil {
		t.Error("accepted oversized remote manifest body")
	}
}

func TestUnitConanPermissionErrors(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := denied(r); got == nil || !strings.Contains(got.Error(), "Unauthorized") {
		t.Fatalf("anonymous Conan denial: %v", got)
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Name: "reader"}))
	if got := denied(r); got == nil || !strings.Contains(got.Error(), "Forbidden") {
		t.Fatalf("authenticated Conan denial: %v", got)
	}
	if !missing(apierror.New(404, "missing")) || missing(apierror.New(403, "denied")) {
		t.Fatal("Conan missing predicate collapsed not-found and denial")
	}
	if _, err := (virtualSource{}).Item(
		t.Context(),
		nil,
		repo.Config{},
		"key",
	); err != source.ErrUnsupported {
		t.Fatalf("virtual source served a direct item: %v", err)
	}
}

func TestUnitConanRemoteMetadataValidation(t *testing.T) {
	for _, tc := range []struct {
		body  string
		size  int64
		valid bool
	}{
		{`{"results":[]}`, 14, true},
		{`[]`, 2, false},
		{`null`, 4, false},
		{`{"results":`, 11, false},
		{`{"results":[]}`, -1, false},
		{`{"results":[]}`, maxRemoteMetadata + 1, false},
	} {
		err := validateJSON(context.Background(), strings.NewReader(tc.body), tc.size)
		if (err == nil) != tc.valid {
			t.Errorf("validateJSON(%q, %d) = %v", tc.body, tc.size, err)
		}
	}
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"hello/1.0", true},
		{"hello/1.0@_/_", true},
		{"../1.0@_/_", false},
		{"hello", false},
		{"hello/1.0@user", false},
		{"hello/1.0@../stable", false},
	} {
		_, ok := parseReference(tc.value)
		if ok != tc.valid {
			t.Errorf("parseReference(%q) = %v", tc.value, ok)
		}
	}
}

func TestUnitConanArchiveValidation(t *testing.T) {
	for _, tc := range []struct {
		name, link, body string
		typeflag         byte
		valid            bool
	}{
		{"conaninfo.txt", "", "hello", tar.TypeReg, true},
		{"metadata/signature", "", "hello", tar.TypeReg, true},
		{"link", "conaninfo.txt", "", tar.TypeSymlink, true},
		{"link", "../../secret", "", tar.TypeSymlink, false},
		{"../escape", "", "hello", tar.TypeReg, false},
		{"/absolute", "", "hello", tar.TypeReg, false},
		{"device", "", "", tar.TypeChar, false},
	} {
		var encoded bytes.Buffer
		gz := gzip.NewWriter(&encoded)
		tw := tar.NewWriter(gz)
		h := &tar.Header{
			Name:     tc.name,
			Linkname: tc.link,
			Typeflag: tc.typeflag,
			Mode:     0o600,
			Size:     int64(len(tc.body)),
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(tc.body)); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		files, err := archiveDigests(bytes.NewReader(encoded.Bytes()))
		if (err == nil) != tc.valid {
			t.Errorf("archiveDigests(%q): %v", tc.name, err)
		}
		if tc.valid && files[tc.name] == "" {
			t.Errorf("missing digest for %q", tc.name)
		}
	}
	if _, err := archiveDigests(strings.NewReader("not gzip")); err == nil {
		t.Error("accepted invalid gzip archive")
	}
	for _, tc := range []struct {
		name    string
		headers []*tar.Header
		valid   bool
	}{
		{"root-directory", []*tar.Header{{Name: "./", Typeflag: tar.TypeDir}, {Name: "conaninfo.txt", Typeflag: tar.TypeReg, Size: 0}}, true},
		{"nested-directory", []*tar.Header{{Name: "metadata/", Typeflag: tar.TypeDir}, {Name: "metadata/signature", Typeflag: tar.TypeReg, Size: 0}}, true},
		{"duplicate", []*tar.Header{{Name: "conaninfo.txt", Typeflag: tar.TypeReg}, {Name: "conaninfo.txt", Typeflag: tar.TypeReg}}, false},
		{"absolute-symlink", []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, false},
	} {
		var encoded bytes.Buffer
		gz := gzip.NewWriter(&encoded)
		tw := tar.NewWriter(gz)
		for _, h := range tc.headers {
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		_, err := archiveDigests(bytes.NewReader(encoded.Bytes()))
		if (err == nil) != tc.valid {
			t.Errorf("%s archive accepted=%v, want %v", tc.name, err == nil, tc.valid)
		}
	}
	for _, tc := range []struct {
		name   string
		files  int
		size   int64
		finish bool
	}{
		{"oversized entry", 1, maxUpload + 1, false},
		{"truncated entry", 1, 5, false},
		{"too many entries", 10001, 0, true},
	} {
		var encoded bytes.Buffer
		gz := gzip.NewWriter(&encoded)
		tw := tar.NewWriter(gz)
		for i := range tc.files {
			if err := tw.WriteHeader(&tar.Header{
				Name: fmt.Sprintf("file%d", i), Typeflag: tar.TypeReg, Size: tc.size,
			}); err != nil {
				t.Fatal(err)
			}
			if tc.size != 0 {
				break
			}
		}
		if tc.finish {
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := archiveDigests(bytes.NewReader(encoded.Bytes())); err == nil {
			t.Errorf("accepted %s", tc.name)
		}
	}
	var malformed bytes.Buffer
	gz := gzip.NewWriter(&malformed)
	if _, err := gz.Write([]byte("not a tar archive")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveDigests(bytes.NewReader(malformed.Bytes())); err == nil {
		t.Error("accepted malformed tar stream")
	}
}
