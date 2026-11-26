package peers_test

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/test/peers"
)

// Temporary diagnosis probe; not a proposed production or acceptance change.
func TestAccAdmissionDeadlineProbe(t *testing.T) {
	t.Setenv("ARTIFACTWEAVE_ADMISSION_PROBE", "1")
	logAdmissionProbePressure(t, "start")
	t.Cleanup(func() { logAdmissionProbePressure(t, "end") })
	for n := range 8 {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			_, all := federation(t, peers.SQLite)
			federate(t, all, "fed-maven", "maven")
			const pom = `<project><modelVersion>4.0.0</modelVersion><groupId>com.example</groupId><artifactId>lib</artifactId><version>1.0</version></project>`
			do(t, all[1], http.MethodPut, "/artifactory/fed-maven/com/example/lib/1.0/lib-1.0.pom", pom, http.StatusCreated)
			do(t, all[1], http.MethodPut, "/artifactory/fed-maven/com/example/lib/1.0/lib-1.0.jar", "jar", http.StatusCreated)
		})
	}
}

func logAdmissionProbePressure(t *testing.T, phase string) {
	t.Helper()
	t.Logf("[DEBUG-1974] phase=%s go=%s os=%s arch=%s cpus=%d gomax=%d", phase, runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	for _, path := range []string{"/proc/pressure/cpu", "/proc/pressure/io", "/sys/fs/cgroup/cpu.stat"} {
		data, err := os.ReadFile(path)
		if err == nil {
			t.Logf("[DEBUG-1974] phase=%s file=%s data=%q", phase, path, data)
		}
	}
}
