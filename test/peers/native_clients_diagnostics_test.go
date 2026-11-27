package peers_test

import "testing"

func TestUnitMavenMissingProduct(t *testing.T) {
	const coordinate = "org.acme.p13:p13-gone-0:jar:1.0.0"
	const repository = "http://127.0.0.1:36073/artifactory/native-maven"
	direct := "Could not find artifact " + coordinate + " in aw (" + repository + ")"
	// Captured from pinned Maven 3.10.0 in both SQLite and PostgreSQL
	// qualification runs. Every consumer uses a new local cache; Resolver can
	// report a cached 404 after trying the same repository within that command.
	cached := coordinate + " was not found in " + repository + " during a previous attempt. " +
		"This failure was cached in the local repository and resolution is not reattempted until the update interval of aw has elapsed or updates are forced"
	for _, tt := range []struct {
		name, output string
		want         bool
	}{
		{"direct_missing", direct, true},
		{"pinned_cached_missing", cached, true},
		{"another_artifact", "Could not find artifact org.acme.p13:another:jar:1.0.0 in aw (" + repository + ")", false},
		{"another_repository", "Could not find artifact " + coordinate + " in aw (" + repository + "-other)", false},
		{"cached_another_group", "x" + coordinate + " was not found in " + repository + " during a previous attempt.", false},
		{"cached_target_after_another_group", "x" + cached + "\n" + cached, true},
		{"cached_another_artifact", "org.acme.p13:another:jar:1.0.0 was not found in " + repository + " during a previous attempt.", false},
		{"cached_another_repository", coordinate + " was not found in " + repository + "-other during a previous attempt.", false},
		{"unauthorized", "Could not transfer artifact " + coordinate + " from/to aw (" + repository + "): status code: 401", false},
		{"unavailable", "Could not transfer artifact " + coordinate + " from/to aw (" + repository + "): status code: 503", false},
		{"cached_transfer_failure", coordinate + " failed to transfer from " + repository + " during a previous attempt. This failure was cached", false},
		{"tool_missing", "mvn: command not found", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := mavenMissingProduct(tt.output, coordinate, repository); got != tt.want {
				t.Fatalf("missing product = %t, want %t: %s", got, tt.want, tt.output)
			}
		})
	}
}
