package config

import (
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/conda"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/helm"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/pypi"
	configapi "github.com/weaveplatform/artifactweave/migration/configuration/api"
)

// Access returns the access module's settings: token.allow_basic_auth and
// environments.global_limit.
func (c Config) Access() accessapi.Settings {
	return accessapi.Settings{
		AllowBasicAuthTokens:   c.Token.AllowBasicAuth,
		GlobalEnvironmentLimit: c.Environments.GlobalLimit,
	}
}

// Artifacts returns the artifacts module's settings from the download,
// search, aql_timeout, build, deploy, npm, cran, huggingface, maven, nuget,
// remote, gc, conda, helm and pypi sections. Formats.TerraformBackend.Keys
// stays nil: the keyring is not configuration, and app sets it.
func (c Config) Artifacts() artifacts.Settings {
	// The pointer is never nil: helm.Settings reads nil as its default,
	// relative URLs, which would silently ignore relative_urls: false.
	relativeURLs := c.Helm.RelativeURLs
	return artifacts.Settings{
		RedirectMinSize:                int64(c.Download.Redirect.MinSize),
		RedirectTTL:                    c.Download.Redirect.TTL,
		FolderDownloadMaxConcurrent:    c.Download.Folder.MaxConcurrentRequests,
		AQLMaxResults:                  c.Search.AQL.MaxResults,
		AQLScanFactor:                  c.Search.AQL.NonAdminScanFactor,
		AQLTimeout:                     c.AQLTimeout.Query,
		AQLStreamTimeout:               c.AQLTimeout.Streamed,
		UserQueryLimit:                 c.Search.UserQueryLimit,
		LimitInternalUsers:             !c.Search.LimitAnonymousUsersOnly,
		AllowDuplicateBuildEntries:     !c.Build.BlockDuplicateEntries,
		EnforceIdenticalChecksumDeploy: c.Deploy.EnforceIdenticalChecksumDeploy,
		NPMLoginTokenExpiry:            c.NPM.LoginTokenExpiry,
		NPMMaxPublishSize:              int64(c.NPM.MaxPublishSize),
		CranArchiveMoverEnabled:        c.Cran.ArchiveMoverEnabled,
		HuggingFaceXetEnabled:          c.HuggingFace.XetEnabled,
		MavenSnapshotCleanupInterval:   c.Maven.SnapshotCleanupInterval,
		NuGetSnapshotCleanupInterval:   c.NuGet.SnapshotCleanupInterval,
		AllowedPrivateOrigins:          c.Remote.AllowedPrivateOrigins,
		GCGrace:                        c.GC.GracePeriod,
		GCInterval:                     c.GC.Interval,
		Formats: ecosystem.Settings{
			Conda: conda.Settings{ShardsEnabled: c.Conda.ShardsEnabled},
			Helm:  helm.Settings{RelativeURLs: &relativeURLs},
			NPM: npm.Settings{
				TagLatestByPublish: c.NPM.TagLatestByPublish,
				EnforcePath:        c.NPM.EnforcePath,
			},
			PyPI: pypi.Settings{
				EnforceLayout:              c.PyPI.EnforceLayout,
				EnforceNamingNormalization: c.PyPI.EnforceNamingNormalization,
				SimpleJSON:                 c.PyPI.SimpleJSON,
				CoreMetadata:               c.PyPI.CoreMetadata,
			},
		},
	}
}

// Configuration returns the configuration module's settings:
// docker.subdomain_match.
func (c Config) Configuration() configapi.Settings {
	return configapi.Settings{DockerSubdomainMatch: c.Docker.SubdomainMatch}
}
