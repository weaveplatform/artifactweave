package repo

import "slices"

// Public returns the subset of c that a caller who can read the repository
// but is not an administrator receives: identity, layout and patterns, with
// nothing that configures access or holds a secret.
func Public(c Config) Config {
	return Config{
		Key: c.Key, Class: c.Class, PackageType: c.PackageType, Description: c.Description,
		RepoLayoutRef: c.RepoLayoutRef, IncludesPattern: c.IncludesPattern,
		ExcludesPattern: c.ExcludesPattern,
	}
}

// Masked returns c without its secrets, for every reader: the remote
// password and the values of sensitive custom headers. An update that omits
// the password, or sends a sensitive header back with an empty value, keeps
// the stored secret (RestoreSecrets). The repositories API's reads send
// Full and the shared remote configuration Descriptor, each of which starts
// from Masked.
//
// It also lists no environments as [] rather than leaving the key out, as
// Artifactory does: the Terraform provider defaults project_environments to an
// empty set and reads an absent key as null, so every plan would show a change.
// It lists no property sets as [] too, as Artifactory's configuration samples
// do, except on a virtual repository, whose configuration has no such field.
//
// A Debian repository that never set ddebSupported or debianTrivialLayout
// reads them as false, the value it acts on (#1372). 7.161.26's
// configuration model (RepositoryConfigurationBase, the base of every
// class's) holds both with the default false; RTFACT-30304 shows
// "ddebSupported" : false in a Debian remote's configuration, and the
// Terraform provider's sample reads (samples/repository_remote/debian.json
// and samples/generic-repo-local.json) give both false. No dump of a
// virtual repository's read was found [I]. The v2 read keeps only what
// Relevant keeps of them.
func Masked(c Config) Config {
	c.Password = ""
	if c.Environments == nil {
		c.Environments = []string{}
	}
	if c.PackageType == Debian {
		if c.DdebSupported == nil {
			c.DdebSupported = new(bool)
		}
		if c.DebianTrivialLayout == nil {
			c.DebianTrivialLayout = new(bool)
		}
	}
	// The provider's repositories has the same empty default and the same
	// null reading of an absent key, so a virtual repository without
	// members lists none as [].
	if c.Class == Virtual && c.Repositories == nil {
		c.Repositories = []string{}
	}
	if c.PropertySets == nil && c.Class != Virtual {
		c.PropertySets = []string{}
	}
	if len(c.CustomHTTPHeaders) > 0 {
		hs := slices.Clone(c.CustomHTTPHeaders)
		for i := range hs {
			if hs[i].Sensitive {
				hs[i].Value = ""
			}
		}
		c.CustomHTTPHeaders = hs
	}
	return c
}

// RestoreSecrets copies stored sensitive header values into updated where
// updated carries the same header back with the empty value Masked gave it,
// so a client that reads a configuration and writes it back does not wipe
// its secrets.
func RestoreSecrets(stored Config, updated *Config) {
	for i, h := range updated.CustomHTTPHeaders {
		if !h.Sensitive || h.Value != "" {
			continue
		}
		for _, s := range stored.CustomHTTPHeaders {
			if s.Name == h.Name && s.Sensitive {
				updated.CustomHTTPHeaders[i].Value = s.Value
			}
		}
	}
}
