package maven_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// eagerWorld is a Maven remote that fetches jars and sources eagerly, over
// an upstream holding app-1.0's POM, jar and sources jar.
func eagerWorld(t *testing.T) *remoteWorld {
	t.Helper()
	w := newMavenRemoteWorld(t)
	w.c.FetchJarsEagerly, w.c.FetchSourcesEagerly = new(true), new(true)
	w.update()
	base := "org/acme/app/1.0/"
	mavenSource(w, base+"app-1.0.pom", []byte(sourcePOM("app", "1.0")), "application/xml")
	mavenSource(w, base+"app-1.0.jar", sourceJAR(t, "main"), "application/java-archive")
	mavenSource(w, base+"app-1.0-sources.jar", sourceJAR(t, "sources"), "application/java-archive")
	return w
}

// TestAccMavenColdHeadSchedulesCompanion (#1475): a cold HEAD of a POM, or
// of a jar, on a remote that fetches eagerly keeps the file (#1391) and
// schedules its companion, as a cold GET does: 7.161.26's
// RemoteRepoBase.doDownloadAndSave runs beforeResourceDownload, which
// starts the eager fetch, before every download, whatever the method.
func TestAccMavenColdHeadSchedulesCompanion(t *testing.T) {
	for _, origin := range []string{"app-1.0.pom", "app-1.0.jar"} {
		for _, method := range []string{"GET", "HEAD"} {
			t.Run(origin+"/"+method, func(t *testing.T) {
				w := eagerWorld(t)
				stopCompanionWorkers(w)
				remoteStatus(
					t,
					w.request(method, "/artifactory/remote/org/acme/app/1.0/"+origin, "reader", ""),
					200,
				)
				if got := pendingCompanions(t, w); got != 1 {
					t.Fatalf("cold %s of %s scheduled %d companions, want 1", method, origin, got)
				}
			})
		}
	}
	// With the workers running, the companion a HEAD schedules is fetched.
	w := eagerWorld(t)
	remoteStatus(
		t,
		w.request("HEAD", "/artifactory/remote/org/acme/app/1.0/app-1.0.pom", "reader", ""),
		200,
	)
	waitMavenRequest(t, w, "/org/acme/app/1.0/app-1.0.jar")
}

// TestAccMavenWarmHeadSchedulesAsWarmGet: a HEAD answered from the cache
// does what a GET answered from the cache does, and schedules the
// companion of a POM the cache kept before the repository fetched eagerly.
func TestAccMavenWarmHeadSchedulesAsWarmGet(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			w := eagerWorld(t)
			w.c.FetchJarsEagerly, w.c.FetchSourcesEagerly = new(false), new(false)
			w.update()
			pom := "/artifactory/remote/org/acme/app/1.0/app-1.0.pom"
			remoteStatus(t, w.request("GET", pom, "reader", ""), 200)
			w.c.FetchJarsEagerly = new(true)
			w.update()
			stopCompanionWorkers(w)
			remoteStatus(t, w.request(method, pom, "reader", ""), 200)
			if got := pendingCompanions(t, w); got != 1 {
				t.Fatalf("warm %s scheduled %d companions, want 1", method, got)
			}
		})
	}
}

// TestAccMavenHeadSchedulesNothingWhereGetDoesNot: the other conditions
// of the schedule hold for a HEAD. A remote that stores nothing keeps
// nothing and schedules nothing, and a cold checksum request keeps
// nothing (#1476) and schedules nothing, as 7.x's respondForChecksumRequest
// never downloads the file.
func TestAccMavenHeadSchedulesNothingWhereGetDoesNot(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		t.Run("no-store/"+method, func(t *testing.T) {
			w := eagerWorld(t)
			w.c.StoreArtifactsLocally = new(false)
			w.update()
			stopCompanionWorkers(w)
			remoteStatus(
				t,
				w.request(method, "/artifactory/remote/org/acme/app/1.0/app-1.0.pom", "reader", ""),
				200,
			)
			if got := pendingCompanions(t, w); got != 0 {
				t.Fatalf("no-store %s scheduled %d companions", method, got)
			}
		})
		t.Run("checksum/"+method, func(t *testing.T) {
			w := eagerWorld(t)
			w.c.RemoteRepoChecksumPolicyType = repo.GenerateIfAbsent
			w.update()
			stopCompanionWorkers(w)
			for _, origin := range []string{"app-1.0.pom", "app-1.0.jar"} {
				remoteStatus(
					t,
					w.request(
						method,
						"/artifactory/remote/org/acme/app/1.0/"+origin+".sha1",
						"reader",
						"",
					),
					404,
				)
			}
			if got := pendingCompanions(t, w); got != 0 {
				t.Fatalf("checksum %s scheduled %d companions", method, got)
			}
		})
	}
}
