package artifactshttp

import (
	"testing"

	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/usage"
)

func TestAccConfiguredCollector(t *testing.T) {
	db := inmem.New()
	cheapSeed(t, db)
	c, err := gc.New(db, nil, gc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := newArtifactoryAPI(Config{Store: db, GC: c})
	if got, err := s.collector(); err != nil || got != c {
		t.Fatalf("collector %p, %v", got, err)
	}
	if _, err := newArtifactoryAPI(Config{}).collector(); err == nil {
		t.Fatal("no store, no collector")
	}
}

func TestAccStorageFormatting(t *testing.T) {
	// 7.x keeps two decimals ("10.00 MB", "47.10 KB" in the captures of
	// research 15) and groups thousands ("1,004.64 KB" in jfrog-cli-core's
	// parser test); TB is its largest unit.
	for n, want := range map[int64]string{
		0: "0 bytes", 101: "101 bytes", 1023: "1023 bytes", 1024: "1.00 KB", 1536: "1.50 KB",
		48230: "47.10 KB", 1028751: "1,004.64 KB", 10 << 20: "10.00 MB",
		3_736_621_547: "3.48 GB", 5 << 40: "5.00 TB", 5 << 50: "5,120.00 TB",
		3 << 60: "3,145,728.00 TB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int64]string{0: "0", 123: "123", 125726: "125,726", 1234567: "1,234,567"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
	for want, got := range map[string]string{
		"33.33%": percent(1, 3), "N/A": percent(1, 0), "100%": percent(4, 4),
		"0.1%": percent(1, 1000), "0%": percent(1, 100000), "20.46%": percent(2046, 10000),
	} {
		if got != want {
			t.Errorf("percent %q, want %q", got, want)
		}
	}
	// A volume that is not measured has no space fields, as 7.x's object
	// stores have none.
	fs := storageSummary(usage.Summary{Location: usage.Location{Type: "file-system"}}).
		FileStoreSummary
	if fs.TotalSpace != nil || fs.UsedSpace != nil || fs.FreeSpace != nil {
		t.Fatalf("file store %+v", fs)
	}
}
