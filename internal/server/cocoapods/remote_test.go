package cocoapods

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func TestUnitRemoteSpecSourceAndIndexValidation(t *testing.T) {
	good := []byte(
		`{"name":"HelloPod","version":"1.0.0","source":{"http":"https://source.example/HelloPod.tar.gz","sha256":"` + strings.Repeat(
			"a",
			64,
		) + `"}}`,
	)
	_, target, checksum, err := sourceDetails(good, "HelloPod", "1.0.0")
	if err != nil || target != "https://source.example/HelloPod.tar.gz" ||
		checksum != strings.Repeat("a", 64) {
		t.Fatalf("valid HTTP source rejected: %q %q %v", target, checksum, err)
	}
	path := podfmt.SpecPath("HelloPod", "1.0.0")
	if err := validateCDN(
		path,
	)(
		t.Context(),
		bytes.NewReader(good),
		int64(len(good)),
		source.MetadataResponse{},
	); err != nil {
		t.Fatalf("valid CDN spec rejected: %v", err)
	}
	for _, data := range [][]byte{
		[]byte(`{"name":"Other","version":"1.0.0","source":{"http":"https://source.example/HelloPod.tar.gz"}}`),
		[]byte(`{"name":"HelloPod","version":"1.0.0","source":{"http":"https://user:pass@source.example/file.tar.gz"}}`),
		[]byte(`{"name":"HelloPod","version":"1.0.0","source":{"http":"https://source.example/file.tar.gz","sha256":"bad"}}`),
		[]byte(`{"name":"HelloPod","version":"1.0.0","source":{"git":"https://git.example/pod.git"}}`),
	} {
		if err := validateCDN(
			path,
		)(
			t.Context(),
			bytes.NewReader(data),
			int64(len(data)),
			source.MetadataResponse{},
		); err == nil {
			t.Fatalf("unsafe CDN spec admitted: %s", data)
		}
	}
	if _, _, _, err := sourceDetails(
		[]byte(
			`{"name":"HelloPod","version":"1.0.0","source":{"git":"https://git.example/pod.git"}}`,
		),
		"HelloPod",
		"1.0.0",
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatalf("Git provider bypass should wait for NAT-07: %v", err)
	}
	index := []byte("HelloPod/1.0.0\n")
	if err := validateCDN(
		podfmt.ShardFile("HelloPod"),
	)(
		t.Context(),
		bytes.NewReader(index),
		int64(len(index)),
		source.MetadataResponse{},
	); err != nil {
		t.Fatalf("valid shard rejected: %v", err)
	}
	wrongShard := []byte("HelloPod/1.0.0\n")
	if err := validateCDN(
		"all_pods_versions_0_0_0.txt",
	)(
		t.Context(),
		bytes.NewReader(wrongShard),
		int64(len(wrongShard)),
		source.MetadataResponse{},
	); err == nil {
		t.Fatal("name in wrong MD5 shard admitted")
	}
	for _, body := range [][]byte{[]byte("no version information"), []byte("min: 1.0.0\nlast: 1.17.0\nprefix_lengths: [0,1,1]\n")} {
		if err := validateCDN(
			"CocoaPods-version.yml",
		)(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
			source.MetadataResponse{},
		); err == nil {
			t.Fatal("invalid CocoaPods CDN version metadata admitted")
		}
	}
}

func TestUnitCDNRedirectGuard(t *testing.T) {
	target, err := url.Parse(
		"https://cdn.cocoapods.org/Specs/b/f/0/HelloPod/1.0.0/HelloPod.podspec.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	guard := cdnGuard(target)
	for _, raw := range []string{
		target.String(),
		"https://cdn.jsdelivr.net/cocoa/Specs/b/f/0/HelloPod/1.0.0/HelloPod.podspec.json",
	} {
		u, _ := url.Parse(raw)
		if err := guard(context.Background(), u); err != nil {
			t.Fatalf("approved CDN URL %s denied: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://cdn.cocoapods.org/Specs/b/f/0/HelloPod/1.0.0/HelloPod.podspec.json",
		"https://cdn.jsdelivr.net/other/Specs/b/f/0/HelloPod/1.0.0/HelloPod.podspec.json",
		"https://cdn.jsdelivr.net/cocoa/Specs/b/f/0/HelloPod/1.0.0/HelloPod.podspec.json?token=x",
	} {
		u, _ := url.Parse(raw)
		if err := guard(context.Background(), u); err == nil {
			t.Fatalf("cross-source redirect %s allowed", raw)
		}
	}
}

func TestUnitSourceRedirectGuard(t *testing.T) {
	target, _ := url.Parse("https://packages.example.org/pod.tar.gz?token=x")
	enabled := true
	c := repo.Config{
		URL:                         "https://packages.example.org/",
		ExternalDependenciesEnabled: &enabled,
		ExternalDependenciesPatterns: []string{
			"https://packages.example.org/**",
			"https://cdn.example.org/pods/**",
		},
	}
	guard := sourceRedirectGuard(c)
	for _, raw := range []string{
		target.String(),
		"https://packages.example.org/moved/pod.tar.gz?token=x",
		"https://cdn.example.org/pods/pod.tar.gz?token=y",
	} {
		u, _ := url.Parse(raw)
		if err := guard(context.Background(), u); err != nil {
			t.Errorf("same-origin source redirect %s denied: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://packages.example.org/moved/pod.tar.gz?token=x",
		"https://other.example.org/moved/pod.tar.gz?token=x",
		"https://cdn.example.org/other/pod.tar.gz?token=x",
		"https://packages.example.org/moved/pod.tar.gz?token=x#fragment",
		"https://user@packages.example.org/moved/pod.tar.gz?token=x",
	} {
		u, _ := url.Parse(raw)
		if err := guard(context.Background(), u); err == nil {
			t.Errorf("unapproved source redirect %s allowed", raw)
		}
	}
	c.AllowAnyHostAuth = &enabled
	cdn, _ := url.Parse("https://cdn.example.org/pods/pod.tar.gz")
	if err := sourceURLAllowed(c, cdn); err == nil {
		t.Fatal("cross-origin source allowed when upstream credentials could be forwarded")
	}
	c.AllowAnyHostAuth = nil
	c.ExternalDependenciesEnabled = nil
	if err := sourceURLAllowed(c, target); err == nil {
		t.Fatal("HTTP source allowed without CocoaPods external dependency opt-in")
	}
}

func TestUnitApprovedCrossOriginSourceTransport(t *testing.T) {
	origin := fakeupstream.New(t, fakeupstream.Options{})
	archive := fakeupstream.New(t, fakeupstream.Options{})
	origin.Set("/pod.tar.gz", fakeupstream.Resource{Redirect: archive.URL + "/approved/pod.tar.gz"})
	archive.Set("/approved/pod.tar.gz", fakeupstream.Resource{Body: []byte("checked archive")})
	enabled := true
	c := repo.Config{
		Key: "pods-remote", Class: repo.Remote, PackageType: repo.CocoaPods,
		URL: origin.URL, Username: "upstream", Password: "upstream-secret",
		ExternalDependenciesEnabled:  &enabled,
		ExternalDependenciesPatterns: []string{origin.URL + "/**", archive.URL + "/approved/**"},
	}
	store := inmem.New()
	if err := store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	c, err := store.Repos().Get(t.Context(), c.Key)
	if err != nil {
		t.Fatal(err)
	}
	request := remote.Request{
		Method: http.MethodGet, ExternalURL: origin.URL + "/pod.tar.gz",
		CheckURL: sourceRedirectGuard(c),
	}
	fetch := func(private []string) error {
		t.Helper()
		resolver, err := remote.NewResolver(store, nil, remote.ResolverOptions{
			RevisionKey: []byte(strings.Repeat("k", 32)), AllowedPrivateOrigins: private,
		})
		if err != nil {
			return err
		}
		options, err := resolver.Options(t.Context(), c)
		if err != nil {
			return err
		}
		client, err := remote.NewClient(c, options)
		if err != nil {
			return err
		}
		defer client.Close()
		response, err := client.Do(t.Context(), request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if string(body) != "checked archive" {
			t.Errorf("wrong archive body: %q", body)
		}
		return nil
	}
	if err := fetch(nil); err == nil || archive.Count(http.MethodGet, "/approved/pod.tar.gz") != 0 {
		t.Fatal("private archive origin reached without operator destination permit")
	}
	if err := fetch([]string{archive.URL}); err != nil {
		t.Fatalf("approved cross-origin archive redirect denied: %v", err)
	}
	requests := archive.Requests()
	if len(requests) != 1 || requests[0].Header.Get("Authorization") != "" ||
		requests[0].Header.Get("Cookie") != "" {
		t.Fatal("cross-origin archive received upstream credentials")
	}
	c.ExternalDependenciesPatterns = []string{origin.URL + "/**"}
	request.CheckURL = sourceRedirectGuard(c)
	if err := fetch(
		[]string{archive.URL},
	); err == nil ||
		archive.Count(http.MethodGet, "/approved/pod.tar.gz") != 1 {
		t.Fatal("archive redirect ignored external dependency pattern")
	}
}

func TestUnitPublicPodNamesRetainPathSafety(t *testing.T) {
	for _, name := range []string{"!ProtoCompiler", "+verify", "-", "ID.me WebVerify", "Floater💩"} {
		if !podfmt.ValidName(name) {
			t.Errorf("authentic CDN name %q rejected", name)
		}
	}
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "a?b", "a\nb"} {
		if podfmt.ValidName(name) {
			t.Errorf("unsafe path segment %q allowed", name)
		}
	}
}
