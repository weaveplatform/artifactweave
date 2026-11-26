package peers_test

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/test/peers"
)

// Temporary diagnosis probe; not a proposed production or acceptance change.
func TestAccAdmissionDeadlineProbe(t *testing.T) {
	for _, repositoryCount := range []int{3, 2, 1} {
		t.Run("repositories-"+strconv.Itoa(repositoryCount), func(t *testing.T) {
			for n := range 8 {
				t.Run(strconv.Itoa(n), func(t *testing.T) {
					t.Parallel()
					_, all := federation(t, peers.SQLite)
					federate(t, all, "fed-maven", "maven")
					if repositoryCount >= 2 {
						federate(t, all, "fed-npm", "npm")
					}
					if repositoryCount >= 3 {
						federate(t, all, "fed-docker", "docker")
					}
					const pom = `<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId>` +
						`<artifactId>lib</artifactId><version>1.0</version></project>`
					do(t, all[1], http.MethodPut, "/artifactory/fed-maven/com/example/lib/1.0/lib-1.0.pom", pom, http.StatusCreated)
					do(t, all[1], http.MethodPut, "/artifactory/fed-maven/com/example/lib/1.0/lib-1.0.jar", "jar", http.StatusCreated)
				})
			}
		})
	}
}
