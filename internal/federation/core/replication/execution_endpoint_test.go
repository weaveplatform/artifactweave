package replication

import "testing"

func TestUnitExecutionDestinationIdentityCanonicalizesEquivalentEndpoints(t *testing.T) {
	for _, row := range []struct{ input, want string }{
		{"https://TARGET.example:443/artifactory/repo/", "https://target.example/artifactory/repo"},
		{"http://TARGET.example:80/artifactory/repo", "http://target.example/artifactory/repo"},
		{"https://[::1]:443/artifactory/repo/", "https://[::1]/artifactory/repo"},
		{"http://[::1]:8081/artifactory/repo", "http://[::1]:8081/artifactory/repo"},
	} {
		got, err := canonicalExecutionURL(row.input)
		if err != nil || got != row.want {
			t.Fatal("equivalent destination changed namespace", got, err)
		}
	}
}
