package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// These are configuration contracts. Native protocol success is covered by
// each P9b format adapter's own status and principal matrix.
func TestP9bRepositoryClassMatrix(t *testing.T) {
	w := newRepoWorld(t)
	types := []struct {
		kind    repo.PackageType
		name    string
		allowed map[repo.Class]bool
	}{
		{repo.LuaRocks, "LuaRocks", map[repo.Class]bool{repo.Local: true, repo.Remote: true, repo.Virtual: true, repo.Federated: true}},
		{repo.NIMModel, "NimModel", map[repo.Class]bool{repo.Remote: true}},
		{repo.Skills, "Skills", map[repo.Class]bool{repo.Local: true}},
		{repo.AgentPlugins, "AgentPlugins", map[repo.Class]bool{repo.Local: true}},
		{repo.AgentPackages, "AgentPackages", map[repo.Class]bool{repo.Local: true, repo.Federated: true}},
	}
	for _, tc := range types {
		for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Virtual, repo.Federated} {
			name := fmt.Sprintf("%s-%s", tc.kind, class)
			t.Run(name, func(t *testing.T) {
				body := fmt.Sprintf(`{"rclass":%q,"packageType":%q}`, class, tc.kind)
				if class == repo.Remote {
					body = fmt.Sprintf(`{"rclass":%q,"packageType":%q,"url":"https://up.example"}`, class, tc.kind)
				}
				rec := w.do("PUT", "/artifactory/api/repositories/"+name, "admin", body)
				if !tc.allowed[class] {
					if rec.Code != 400 || !strings.Contains(rec.Body.String(), "packageType") {
						t.Fatalf("unsupported class: %d %s", rec.Code, rec.Body)
					}
					return
				}
				if rec.Code != 200 {
					t.Fatalf("create: %d %s", rec.Code, rec.Body)
				}
				var got repo.Config
				rec = w.do("GET", "/artifactory/api/repositories/"+name, "admin", "")
				jsonBody(t, rec, &got)
				if rec.Code != 200 || got.Class != class || got.PackageType != tc.kind {
					t.Fatalf("read: %d %+v", rec.Code, got)
				}
				rec = w.do("POST", "/artifactory/api/repositories/"+name, "admin", `{"description":"updated"}`)
				if rec.Code != 200 || w.stored(name).PackageType != tc.kind {
					t.Fatalf("update: %d %s", rec.Code, rec.Body)
				}
				var listed []struct{ Key, PackageType string }
				rec = w.do("GET", "/artifactory/api/repositories?packageType="+string(tc.kind), "reader", "")
				if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, c := range listed {
					if c.Key == name && c.PackageType == tc.name {
						found = true
					}
				}
				if rec.Code != 200 || !found {
					t.Fatalf("list: %d %+v", rec.Code, listed)
				}
			})
		}
	}
}
