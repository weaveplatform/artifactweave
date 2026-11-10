package vcshttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func TestUnitVCSPeerEndpointAndPayloadContracts(t *testing.T) {
	const oid = "0123456789abcdef0123456789abcdef01234567"
	c := repo.Config{
		URL:            "https://peer.example/context/artifactory/api/vcs/source/",
		VCSGitProvider: repo.ArtifactoryVCS,
	}
	endpoints, err := RefEndpoints(c, "team", "project")
	if err != nil || len(endpoints) != 2 {
		t.Fatalf("peer endpoints: %v %v", endpoints, err)
	}
	for i, action := range []string{"tags", "branches"} {
		t.Run(action, func(t *testing.T) {
			e := endpoints[i]
			want := "https://peer.example/context/artifactory/api/vcs/" + action + "/source/team/project"
			if e.URL.String() != want || e.Variant() != "vcs-peer-"+action+"-v1" {
				t.Fatalf("peer endpoint = %s, %s", e.URL, e.Variant())
			}
			data, _ := json.Marshal(
				[]map[string]any{{"name": "release/one", "commitId": oid, "isBranch": i == 1}},
			)
			refs, err := e.Parse(data)
			if err != nil || len(refs) != 1 || refs[0].Name != "release/one" ||
				refs[0].CommitID != oid ||
				refs[0].Branch != (i == 1) {
				t.Fatalf("peer parse = %+v %v", refs, err)
			}
		})
	}
	u, err := ArchiveURL(c, "team", "project", oid, "zip")
	if err != nil ||
		u.String() != "https://peer.example/context/artifactory/api/vcs/downloadCommit/source/team/project/"+oid+"?ext=zip" {
		t.Fatalf("peer archive = %v %v", u, err)
	}
	if _, err := ArchiveURL(c, "team", "project", "main", "zip"); !errors.Is(err, ErrInvalidPath) {
		t.Fatal("peer archive accepted mutable ref")
	}
	for _, raw := range []string{"https://peer.example/artifactory/source", "https://peer.example/api/vcs/source/other", "https://peer.example/api/vcs/../source", "https://user:pass@peer.example/api/vcs/source"} {
		t.Run(raw, func(t *testing.T) {
			c.URL = raw
			if _, err := RefEndpoints(c, "team", "project"); !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("accepted invalid peer base: %v", err)
			}
			if _, err := ArchiveURL(
				c,
				"team",
				"project",
				oid,
				"zip",
			); !errors.Is(
				err,
				ErrInvalidPath,
			) {
				t.Fatalf("accepted invalid peer archive base: %v", err)
			}
		})
	}
	c.URL = "https://peer.example/api/vcs/source"
	if _, err := RefEndpoints(c, "team/sub", "project"); !errors.Is(err, ErrInvalidPath) {
		t.Fatal("peer accepted unsupported subgroup")
	}
	for _, c := range []repo.Config{{URL: "https://git.example"}, {URL: "bad://host"}} {
		got, err := RefEndpoints(c, "team", "project")
		if c.URL == "bad://host" {
			if !errors.Is(err, ErrInvalidPath) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || len(got) != 1 || got[0].Variant() != "vcs-git-refs-v1" {
			t.Fatalf("Git endpoint %v %v", got, err)
		}
		if _, err := got[0].Parse([]byte("0000")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnitVCSPeerRejectsMalformedOrAmbiguousReferences(t *testing.T) {
	const oid = "0123456789abcdef0123456789abcdef01234567"
	valid := `{"name":"main","commitId":"` + oid + `","isBranch":true}`
	var many strings.Builder
	many.WriteString("[")
	for i := 0; i <= MaxRefs; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"name":"v%d","commitId":"%s","isBranch":true}`, i, oid)
	}
	many.WriteString("]")
	for name, data := range map[string]string{
		"empty": "", "null": "null", "object": "{}", "truncated": "[", "broken row": "[1]", "trailing": "[] {}",
		"missing name":   `[{"commitId":"` + oid + `","isBranch":true}]`,
		"missing branch": `[{"name":"main","commitId":"` + oid + `"}]`,
		"wrong kind":     strings.Replace("["+valid+"]", "true", "false", 1),
		"wrong oid":      strings.Replace("["+valid+"]", oid, "abcdef", 1),
		"invalid name":   strings.Replace("["+valid+"]", "main", "../secret", 1),
		"duplicate ref":  "[" + valid + "," + valid + "]",
		"too many":       many.String(),
		"oversize":       strings.Repeat(" ", MaxRefsBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePeerRefs([]byte(data), true); !errors.Is(err, ErrInvalidRefs) {
				t.Fatalf("accepted invalid refs: %v", err)
			}
		})
	}
	if refs, err := parsePeerRefs([]byte("[]"), false); err != nil || len(refs) != 0 {
		t.Fatalf("empty refs = %v %v", refs, err)
	}
}

func TestUnitVCSPeerSelectedFileURLBindsReferenceAndPath(t *testing.T) {
	c := repo.Config{
		URL:            "https://peer.example/context/api/vcs/source",
		VCSGitProvider: repo.ArtifactoryVCS,
	}
	for _, tc := range []struct{ kind, action string }{
		{"tags", "downloadTagFile"}, {"heads", "downloadBranchFile"},
	} {
		got, err := PeerFileURL(c, "team", "project", tc.kind, "release/one", "sub/README.md")
		want := "https://peer.example/context/api/vcs/" + tc.action + "/source/team/project/release/one%21sub/README.md?ext=zip"
		if err != nil || got.String() != want {
			t.Fatalf("file URL: %v %v", got, err)
		}
	}
	for _, tc := range []struct{ base, kind, ref, file string }{
		{c.URL, "commits", "main", "README.md"},
		{c.URL, "tags", "../main", "README.md"},
		{c.URL, "tags", "main", "../README.md"},
		{"https://peer.example/source", "heads", "main", "README.md"},
	} {
		c.URL = tc.base
		if _, err := PeerFileURL(
			c,
			"team",
			"project",
			tc.kind,
			tc.ref,
			tc.file,
		); !errors.Is(
			err,
			ErrInvalidPath,
		) {
			t.Fatalf("invalid file URL accepted: %+v %v", tc, err)
		}
	}
}
