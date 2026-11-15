package repo

import "slices"

// Full returns what an administrator's v1 read, GET /api/repositories/{key},
// sends of c: Masked(c) completed to 7.161.26's read model of c's class
// (LocalRepositoryConfigurationImpl for a local repository and, through it,
// FederatedRepositoryConfigurationImpl; HttpRepositoryConfigurationImpl;
// VirtualRepositoryConfigurationImpl; each extends
// RepositoryConfigurationBase). The v1 read writes every field of the
// model, whatever the package type, as its dumps of Docker, npm and Maven
// local repositories (RTFACT-29415, RTFACT-30672, RTFACT-31100) and the
// Terraform provider's sample reads of generic local and remote
// repositories and remotes of other types (samples/generic-repo-local.json,
// samples/repository_remote) show. A field the model reads only from the
// configuration of its own package types has its default for every other
// type, whatever was stored.
//
// The model's fields that artifactweave does not hold are left out [I]: the
// nested per-type settings of a remote (gitLabResolveSubgroups,
// podsForceModifyIndex, authUrl and the other types' registry URLs),
// gridTopologyId and federationOnGrid. No dump of a remote, virtual or
// federated repository's v1 read from 7.161.26 was found, so that each
// carries what its model holds is inferred [I]. The v2 read and the
// configurations list keep what Relevant keeps of Full.
func Full(c Config) Config {
	// A stored password stays out of the read, as Masked leaves it, so
	// that a client that writes the read back keeps it (an update that
	// sends "" clears it). 7.x's read of a remote with a password is not
	// known [I]; with none, it is "" in every sample read.
	secret := c.Password != ""
	c = Masked(c)
	setTypeFlags(&c)
	baseModel(&c)
	switch c.Class {
	case Local, Federated:
		localModel(&c)
	case Remote:
		remoteModel(&c)
		if !secret {
			c.show("password")
		}
	case Virtual:
		virtualModel(&c)
	}
	return c
}

// baseModel completes what RepositoryConfigurationBase holds for every
// class:
//
//   - forceNugetAuthentication, forceP2Authentication and
//     forceConanAuthentication, taken from the configuration of a NuGet, P2
//     or Conan repository and false for every other type, and
//     enableNormalizedVersion, taken from a NuGet repository's
//     (setPackageTypeConfig sets each for its own type only);
//   - ddebSupported and debianTrivialLayout, taken from a Debian
//     repository's configuration (Masked) and false for every other type
//     (LocalRepositoryConfigurationImpl.setDebianConfigIfNeeded);
//   - signedUrlTtl, 90 when unset, which the configurations list's sample
//     shows too ([D] getallrepositoryconfigurations);
//   - dockerApiVersion V2 and blockPushingSchema1 true, which only a Docker,
//     OCI or Helm OCI repository takes from its configuration, and of those
//     only a local, remote or federated one blockPushingSchema1;
//   - dockerProjectId "", which a Docker, OCI or Helm OCI repository takes
//     from its configuration instead, and so leaves out when it has none, as
//     RTFACT-29415's Docker local read does;
//   - forceNonDuplicateChart and forceMetadataNameVersion false but for a
//     local or federated Helm repository (setEnforceLayoutConfigIfNeeded),
//     and mlRepoLayout false but for a Hugging Face repository;
//   - priorityResolution false.
func baseModel(c *Config) {
	docker := isDocker(c.PackageType)
	for own, flag := range map[PackageType]**bool{
		NuGet: &c.ForceNugetAuthentication,
		P2:    &c.ForceP2Authentication,
		Conan: &c.ForceConanAuthentication,
	} {
		ownOr(flag, c.PackageType == own, false)
	}
	ownOr(&c.EnableNormalizedVersion, c.PackageType == NuGet, false)
	if c.PackageType != Debian {
		c.DdebSupported, c.DebianTrivialLayout = new(false), new(false)
	}
	if c.SignedURLTTL == nil {
		c.SignedURLTTL = new(DefaultSignedURLTTL)
	}
	if !docker || c.DockerAPIVersion == "" {
		c.DockerAPIVersion = DockerV2
	}
	if !docker || c.Class == Virtual || c.BlockPushingSchema1 == nil {
		c.BlockPushingSchema1 = Flex(true)
	}
	if !docker {
		c.DockerProjectID = ""
		c.show("dockerProjectId")
	}
	helmHosted := c.PackageType == Helm && (c.Class == Local || c.Class == Federated)
	ownOr(&c.ForceNonDuplicateChart, helmHosted, false)
	ownOr(&c.ForceMetadataNameVersion, helmHosted, false)
	ownOr(&c.MLRepoLayout, c.PackageType == HuggingFaceML, false)
	setFlex(&c.PriorityResolution, false)
}

// localModel completes what LocalRepositoryConfigurationImpl holds, for a
// local or federated repository: xrayDataTtl 90 (DEFAULT_DATA_TTL);
// calculateYumMetadata, enableFileListsIndexing and yumRootDepth, which
// only an RPM repository takes from its configuration, and
// yumGroupFileNames, which no other has; enableComposerV1Indexing, which
// only a Composer repository takes; terraformType MODULE, which a Terraform
// repository takes from its configuration instead; encryptStates true,
// which only a Terraform backend repository takes; and cargoAnonymousAccess
// false and cargoInternalIndex true, which only a Cargo repository takes,
// the latter true when unset (isCargoInternalIndex; the Maven local read of
// 7.104.10 in RTFACT-31100 gave false). A federated repository's model
// also holds disableProxy, false by default
// (FederatedRepositoryConfigurationImpl).
func localModel(c *Config) {
	setInt(&c.XrayDataTTL, DefaultXrayDataTTL)
	rpm := c.PackageType == RPM
	ownOr(&c.CalculateYumMetadata, rpm, false)
	if !rpm || c.EnableFileListsIndexing == nil {
		c.EnableFileListsIndexing = Flex(false)
	}
	if !rpm || c.YumRootDepth == nil {
		c.YumRootDepth = new(0)
	}
	if !rpm {
		c.YumGroupFileNames = ""
	}
	ownOr(&c.EnableComposerV1Indexing, c.PackageType == Composer, false)
	if c.PackageType != Terraform {
		c.TerraformType = TerraformModule
	}
	ownOr(&c.EncryptStates, c.PackageType == TerraformBackend, true)
	cargoModel(c)
	if c.Class == Federated {
		setBool(&c.DisableProxy, false)
	}
}

// remoteModel completes what HttpRepositoryConfigurationImpl holds for
// every remote repository: username and localAddress "" (Full adds the
// password, "" too, for a remote that has none), customHttpHeaders []
// (setCustomHttpHeadersFromRepoConfig), maxUniqueTags 0,
// archiveBrowsingEnabled, propagateQueryParams, retrieveSha256FromServer,
// passThrough, curated and sendContext false, xrayDataTtl 90, and the Cargo
// settings as a local repository has them. contentSynchronisation, when
// unset, is the descriptor's default, all off, as every remote sample read
// of the Terraform provider gives it.
func remoteModel(c *Config) {
	c.show("username", "localAddress", "customHttpHeaders")
	setInt(&c.MaxUniqueTags, 0)
	for _, p := range []**bool{
		&c.ArchiveBrowsingEnabled, &c.PropagateQueryParams, &c.RetrieveSha256FromServer,
		&c.PassThrough, &c.Curated, &c.SendContext,
	} {
		setBool(p, false)
	}
	if c.ContentSynchronisation == nil {
		c.ContentSynchronisation = &ContentSynchronisation{
			Statistics: &EnabledFlag{}, Properties: &EnabledFlag{}, Source: &ContentSyncSource{},
		}
	}
	setInt(&c.XrayDataTTL, DefaultXrayDataTTL)
	cargoModel(c)
}

// virtualModel completes what VirtualRepositoryConfigurationImpl holds:
// keyPair "", cachingLocalForeignLayersEnabled false but for a Docker,
// OCI or Helm OCI repository, cargoAnonymousAccess false but for a Cargo
// repository, and cargoInternalIndex, always true. The model has no
// blackedOut, xrayIndex, xrayDataTtl, downloadRedirect or cdnRedirect, so
// the read leaves them out whatever was stored.
func virtualModel(c *Config) {
	c.BlackedOut, c.XrayIndex, c.XrayDataTTL = nil, nil, nil
	c.DownloadRedirect, c.CDNRedirect = nil, nil
	c.show("keyPair")
	ownOr(&c.CachingLocalForeignLayersEnabled, isDocker(c.PackageType), false)
	ownOr(&c.CargoAnonymousAccess, c.PackageType == Cargo, false)
	c.CargoInternalIndex = new(true)
}

// cargoModel gives a local, federated or remote repository the Cargo
// settings of its model: those a Cargo repository stored, with
// cargoAnonymousAccess false and cargoInternalIndex true when unset, and
// those defaults for every other type.
func cargoModel(c *Config) {
	cargo := c.PackageType == Cargo
	ownOr(&c.CargoAnonymousAccess, cargo, false)
	ownOr(&c.CargoInternalIndex, cargo, true)
}

// ownOr keeps *p for a repository whose package type the read model takes
// the setting from (own), giving it v when nothing was stored, and sets v
// for every other.
func ownOr(p **bool, own, v bool) {
	if !own || *p == nil {
		*p = new(v)
	}
}

// show adds names to the fields c's read writes even when empty (Shown).
func (c *Config) show(names ...string) {
	c.Shown = append(slices.Clone(c.Shown), names...)
}

// isDocker reports the package types 7.x serves with its Docker registry
// API (RepoType.isDockerGroup).
func isDocker(t PackageType) bool { return t == Docker || t == OCI || t == HelmOCI }
