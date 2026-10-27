package config

// PyPI holds the PyPI repositories' options (decision 0037). Each is
// server-wide, as its 7.x counterpart is a system property or a global
// setting with no field on the repository, and each is off by default, as
// there.
type PyPI struct {
	// EnforceLayout refuses the upload of a distribution whose file name
	// does not begin with its metadata's Name and Version, as
	// artifactory.pypi.enforce.layout does.
	EnforceLayout bool `yaml:"enforce_layout"`
	// EnforceNamingNormalization refuses the upload of a distribution
	// whose file name's name segment holds a "-", as
	// artifactory.pypi.enforce.naming.normalization does.
	EnforceNamingNormalization bool `yaml:"enforce_naming_normalization"`
	// SimpleJSON answers the simple index in PEP 691 JSON to a client that
	// asks for it, as "Enable simple json format" does from 7.146.7.
	SimpleJSON bool `yaml:"simple_json"`
	// CoreMetadata advertises and serves the wheels' core metadata files
	// (PEP 658), as the system property behind "Fast Dependency
	// Resolution" does (RTFACT-31470).
	CoreMetadata bool `yaml:"core_metadata"`
}
