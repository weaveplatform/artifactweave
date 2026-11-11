package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
)

// TestUnitModuleSettingsFromConfig checks each module setting against the key
// it maps, over testdata/every-key.yaml, whose values are not the defaults.
// The file holds exactly what TestUnitFileCoversEveryKey expects, so the
// module keys it leaves out come from flags, also off their defaults.
func TestUnitModuleSettingsFromConfig(t *testing.T) {
	cfg, err := Load(LoadOptions{
		File: filepath.Join("testdata", "every-key.yaml"),
		Flags: map[string]string{
			"gc.interval":                       "7m",
			"gc.grace_period":                   "3h",
			"npm.enforce_path":                  "true",
			"cran.archive_mover_enabled":        "true",
			"huggingface.xet_enabled":           "true",
			"conda.shards_enabled":              "true",
			"pypi.enforce_layout":               "true",
			"pypi.enforce_naming_normalization": "true",
			"pypi.simple_json":                  "true",
			"pypi.core_metadata":                "true",
			"remote.allowed_private_origins":    "https://mirror.example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(
		accessapi.Settings{AllowBasicAuthTokens: true, GlobalEnvironmentLimit: 30},
		cfg.Access(),
	); diff != "" {
		t.Errorf("Access (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(
		configapi.Settings{DockerSubdomainMatch: true},
		cfg.Configuration(),
	); diff != "" {
		t.Errorf("Configuration (-want +got):\n%s", diff)
	}
	relativeURLs := false
	want := artifacts.Settings{
		RedirectMinSize:             4 << 20,
		RedirectTTL:                 90 * time.Second,
		FolderDownloadMaxConcurrent: 3,
		AQLMaxResults:               1000,
		AQLScanFactor:               3,
		AQLTimeout:                  10 * time.Minute,
		AQLStreamTimeout:            time.Hour,
		UserQueryLimit:              50,
		// The two negations: the file sets limit_anonymous_users_only and
		// block_duplicate_entries to false.
		LimitInternalUsers:             true,
		AllowDuplicateBuildEntries:     true,
		EnforceIdenticalChecksumDeploy: true,
		NPMLoginTokenExpiry:            48 * time.Hour,
		NPMMaxPublishSize:              256 << 20,
		CranArchiveMoverEnabled:        true,
		HuggingFaceXetEnabled:          true,
		MavenSnapshotCleanupInterval:   2 * time.Minute,
		NuGetSnapshotCleanupInterval:   3 * time.Minute,
		AllowedPrivateOrigins:          []string{"https://mirror.example.com"},
		GCGrace:                        3 * time.Hour,
		GCInterval:                     7 * time.Minute,
		Formats: ecosystem.Settings{
			Conda: conda.Settings{ShardsEnabled: true},
			// relative_urls: false, which only a non-nil pointer carries.
			Helm: helm.Settings{RelativeURLs: &relativeURLs},
			NPM:  npm.Settings{TagLatestByPublish: true, EnforcePath: true},
			PyPI: pypi.Settings{
				EnforceLayout:              true,
				EnforceNamingNormalization: true,
				SimpleJSON:                 true,
				CoreMetadata:               true,
			},
		},
	}
	if diff := cmp.Diff(want, cfg.Artifacts()); diff != "" {
		t.Errorf("Artifacts (-want +got):\n%s", diff)
	}

	// The defaults turn both negations off and keep the Helm pointer set.
	def := Default().Artifacts()
	if def.LimitInternalUsers || def.AllowDuplicateBuildEntries {
		t.Errorf(
			"default LimitInternalUsers, AllowDuplicateBuildEntries = %t, %t; want false, false",
			def.LimitInternalUsers,
			def.AllowDuplicateBuildEntries,
		)
	}
	if r := def.Formats.Helm.RelativeURLs; r == nil || !*r {
		t.Errorf("default Formats.Helm.RelativeURLs = %v, want a pointer to true", r)
	}
}
