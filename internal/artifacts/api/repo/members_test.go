package repo_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// 7.x's member URL rules (research 40 section 3): the key is the last path
// element and the base is normalized to end in /artifactory.
func TestUnitSplitMemberURL(t *testing.T) {
	for _, tc := range []struct{ raw, base, key string }{
		{"https://b.example/artifactory/fed", "https://b.example/artifactory", "fed"},
		{"https://b.example/fed/", "https://b.example/artifactory", "fed"},
		{"http://b.example:8082/prefix/artifactory/fed", "http://b.example:8082/prefix/artifactory", "fed"},
		{"https://b.example/artifactory//fed", "https://b.example/artifactory", "fed"},
		{"{self}/fed", repo.SelfMemberBase, "fed"},
	} {
		base, key, err := repo.SplitMemberURL(tc.raw)
		if err != nil || base != tc.base || key != tc.key {
			t.Errorf("SplitMemberURL(%q) = %q %q %v, want %q %q", tc.raw, base, key, err, tc.base, tc.key)
		}
	}
	for _, raw := range []string{
		"", "not a url", "ftp://b.example/fed", "https://b.example", "https://b.example/",
		"https://u:p@b.example/fed", "https://b.example/fed?x=1", "https://b.example/fed#f",
		"mailto:x@b.example", "{self}/", "/artifactory/fed",
	} {
		if _, _, err := repo.SplitMemberURL(raw); err == nil {
			t.Errorf("SplitMemberURL(%q) accepted", raw)
		}
	}
	if got := repo.NormalizeMemberBase("https://b.example/artifactory//"); got != "https://b.example/artifactory" {
		t.Errorf("NormalizeMemberBase = %q", got)
	}
	if got := repo.MemberURL("https://b.example/artifactory", "fed"); got != "https://b.example/artifactory/fed" {
		t.Errorf("MemberURL = %q", got)
	}
}

func TestUnitResolveSelfMember(t *testing.T) {
	c := repo.Config{Key: "fed", Members: []repo.Member{{URL: "https://b.example/artifactory/fed"}, {URL: repo.SelfMemberURL("fed")}}}
	stored := c.Members
	repo.ResolveSelfMember(&c, "https://a.example/")
	if got := []string{c.Members[0].URL, c.Members[1].URL}; !slices.Equal(got, []string{
		"https://b.example/artifactory/fed", "https://a.example/artifactory/fed",
	}) {
		t.Fatalf("resolved %v", got)
	}
	if stored[1].URL != repo.SelfMemberURL("fed") {
		t.Fatal("ResolveSelfMember changed the stored members")
	}
	// Without a base URL the placeholder is left for the caller to see.
	c = repo.Config{Key: "fed", Members: []repo.Member{{URL: repo.SelfMemberURL("fed")}}}
	repo.ResolveSelfMember(&c, "")
	if c.Members[0].URL != repo.SelfMemberURL("fed") {
		t.Fatalf("resolved without a base %v", c.Members)
	}
}

func federated(members string) repo.Config {
	var c repo.Config
	if err := json.Unmarshal([]byte(`{"key":"fed","rclass":"federated","packageType":"generic","members":`+members+`}`), &c); err != nil {
		panic(err)
	}
	repo.Defaults(&c)
	return c
}

func TestUnitValidateMembers(t *testing.T) {
	ok := federated(`[{"url":"https://b.example/fed","enabled":"false"},{"url":"https://c.example/fed","mode":"bidirectional","enabled":false}]`)
	if err := repo.Validate(&ok); err != nil {
		t.Fatal(err)
	}
	if ok.Members[0].Enabled.Bool() || ok.Members[1].Enabled.Bool() {
		t.Fatal(`enabled "false" and false read as true`)
	}
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = `{"url":"https://b.example/fed` + string(rune('a'+i)) + `"}`
	}
	for _, tc := range []struct{ members, want string }{
		{"[" + strings.Join(eleven, ",") + "]", "members: allows at most 10 members, this instance's included"},
		{`[{"url":"nope"}]`, "members.url"},
		{`[{"url":"https://b.example/fed","mode":"receiver"}]`, `members.mode: "receiver" is not supported yet; use bidirectional`},
		{`[{"url":"https://b.example/fed","mode":"unidirectional"}]`, `members.mode: "unidirectional" is not one of bidirectional, receiver`},
	} {
		c := federated(tc.members)
		if err := repo.Validate(&c); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.members, err, tc.want)
		}
	}
}
