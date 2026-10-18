package repo_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestAcceptsMember(t *testing.T) {
	tests := []struct {
		virtual, member repo.PackageType
		want            bool
	}{
		{repo.NPM, repo.NPM, true},
		{repo.NPM, repo.Maven, false},
		{repo.NPM, repo.Generic, false},
		// A generic virtual repository aggregates every type.
		{repo.Generic, repo.NPM, true},
		{repo.Generic, repo.Docker, true},
		// The Maven family mixes, both ways.
		{repo.Maven, repo.Gradle, true},
		{repo.Gradle, repo.Maven, true},
		{repo.Ivy, repo.SBT, true},
		{repo.SBT, repo.P2, true},
		{repo.Maven, repo.NPM, false},
		{repo.Maven, repo.Generic, false},
		// Helm and Helm OCI cannot share a virtual repository, nor Docker and OCI.
		{repo.Helm, repo.HelmOCI, false},
		{repo.Docker, repo.OCI, false},
		{repo.OCI, repo.Docker, false},
	}
	for _, tt := range tests {
		if got := repo.AcceptsMember(tt.virtual, tt.member); got != tt.want {
			t.Errorf("AcceptsMember(%s, %s) = %v, want %v", tt.virtual, tt.member, got, tt.want)
		}
	}
}

// The Terraform provider defaults repositories to an empty list and reads an
// absent key as null, so a virtual repository without members must show [].
func TestVirtualWithoutMembersRendersAnEmptyList(t *testing.T) {
	c := repo.Config{Key: "v", Class: repo.Virtual, PackageType: repo.NPM}
	stored, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "repositories") {
		t.Errorf("stored document gained repositories: %s", stored)
	}
	shown, err := json.Marshal(repo.Masked(c))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shown), `"repositories":[]`) {
		t.Errorf("want repositories [] in %s", shown)
	}
	c.Repositories = []string{"a"}
	if shown, _ = json.Marshal(repo.Masked(c)); !strings.Contains(
		string(shown),
		`"repositories":["a"]`,
	) {
		t.Errorf("want repositories [a] in %s", shown)
	}
	// Other classes have no members to show.
	local, _ := json.Marshal(repo.Masked(repo.Config{Key: "l", Class: repo.Local}))
	if strings.Contains(string(local), "repositories") {
		t.Errorf("a local repository shows repositories: %s", local)
	}
	var back repo.Config
	if err := json.Unmarshal(shown, &back); err != nil || len(back.Repositories) != 1 {
		t.Fatalf("round trip %+v %v", back, err)
	}
}
