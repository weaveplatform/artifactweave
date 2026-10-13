package types

import (
	"path"
	"slices"
	"strings"
	"time"
)

// BundleState is a release bundle version's lifecycle state (decision 0023).
type BundleState string

// Bundle states. OPEN and SIGNED are Distribution's documented values
// (research 17); STORED records that the signed version was also written to its
// storing repository, which Distribution reports through storing_repository
// rather than a state of its own.
const (
	BundleOpen   BundleState = "OPEN"
	BundleSigned BundleState = "SIGNED"
	BundleStored BundleState = "STORED"
)

// Editable reports whether the bundle can still be changed; signing freezes it.
func (s BundleState) Editable() bool { return s == BundleOpen }

// BundleOrigin says whether this instance created the bundle or received it.
type BundleOrigin string

// Bundle origins.
const (
	BundleSource   BundleOrigin = "source"
	BundleReceived BundleOrigin = "received"
)

// PropertyValues is one key and its values in a bundle spec or artifact.
type PropertyValues struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// PathMapping rewrites a source path to a target path; Output may refer to
// capture groups of Input.
type PathMapping struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

// BundleQuery selects artifacts for a bundle by AQL.
type BundleQuery struct {
	Name                 string           `json:"name,omitempty"`
	AQL                  string           `json:"aql"`
	Mappings             []PathMapping    `json:"mappings,omitempty"`
	AddedProps           []PropertyValues `json:"added_props,omitempty"`
	ExcludePropsPatterns []string         `json:"exclude_props_patterns,omitempty"`
}

// BundleSpec is the creation spec, kept as a document so the bundle records
// how it was built.
type BundleSpec struct {
	Queries []BundleQuery `json:"queries"`
}

// ReleaseNotes accompany a bundle version.
type ReleaseNotes struct {
	Syntax  string `json:"syntax,omitempty"`
	Content string `json:"content,omitempty"`
}

// ReleaseBundle is one version of a release bundle v1.
type ReleaseBundle struct {
	Name              string
	Version           string
	Description       string
	Notes             ReleaseNotes
	StoringRepository string
	State             BundleState
	Spec              BundleSpec
	ProjectKey        string
	Origin            BundleOrigin
	Created           time.Time
	CreatedBy         string
	SignedAt          time.Time
	SigningKeyAlias   string
	Signature         []byte
}

// BundleArtifact is one frozen entry of a bundle's artifact list.
type BundleArtifact struct {
	BundleName    string
	BundleVersion string
	SourcePath    string
	TargetPath    string
	Checksums     Checksums
	Size          int64
	Props         []PropertyValues
}

// SigningKey signs bundle manifests. The private key and passphrase are
// sealed and never serialised.
type SigningKey struct {
	Alias      string
	PublicKey  string
	PrivateKey string `json:"-"`
	Passphrase string `json:"-"`
	IsDefault  bool
}

// DistributionTarget is an edge: a peer paired for distribution, with the
// location rules select on.
type DistributionTarget struct {
	PeerInstanceID string
	SiteName       string
	CityName       string
	CountryCode    string
}

// DistributionRule selects targets; empty fields match everything. SiteName
// accepts '*' wildcards as JFrog's rules do.
type DistributionRule struct {
	SiteName     string   `json:"site_name,omitempty"`
	CityName     string   `json:"city_name,omitempty"`
	CountryCodes []string `json:"country_codes,omitempty"`
	Priority     int      `json:"priority,omitempty"`
}

// Matches reports whether t satisfies the rule.
func (r DistributionRule) Matches(t DistributionTarget) bool {
	if r.SiteName != "" {
		ok, err := path.Match(r.SiteName, t.SiteName)
		if err != nil || !ok {
			return false
		}
	}
	if r.CityName != "" && !strings.EqualFold(r.CityName, t.CityName) {
		return false
	}
	if len(r.CountryCodes) > 0 && !slices.ContainsFunc(r.CountryCodes, func(c string) bool {
		return strings.EqualFold(c, t.CountryCode)
	}) {
		return false
	}
	return true
}

// SelectTargets returns the targets any rule matches, in target order.
func SelectTargets(rules []DistributionRule, targets []DistributionTarget) []DistributionTarget {
	var out []DistributionTarget
	for _, t := range targets {
		if slices.ContainsFunc(rules, func(r DistributionRule) bool { return r.Matches(t) }) {
			out = append(out, t)
		}
	}
	return out
}

// TrackerKind is what a distribution tracker does.
type TrackerKind string

// Tracker kinds.
const (
	TrackerDistribute TrackerKind = "distribute"
	TrackerDelete     TrackerKind = "delete"
)

// JobStatus is the state of a tracker or of one per-target job. The strings
// are Distribution's display values as jfrog-client-go and the archived REST
// examples show them (research 17); Aborted is artifactweave's, reported after
// an abort request because no published example shows that state.
type JobStatus string

// Job statuses.
const (
	JobNotDistributed JobStatus = "Not distributed"
	JobInQueue        JobStatus = "In queue"
	JobInProgress     JobStatus = "In progress"
	JobCompleted      JobStatus = "Completed"
	JobFailed         JobStatus = "Failed"
	JobAborted        JobStatus = "Aborted"
)

// DistributionTracker is one distribute or delete request (int64 id on the
// wire) with its selection rules.
type DistributionTracker struct {
	ID                            int64
	BundleName                    string
	BundleVersion                 string
	Kind                          TrackerKind
	DryRun                        bool
	AutoCreateMissingRepositories bool
	Rules                         []DistributionRule
	Status                        JobStatus
	Created                       time.Time
	CreatedBy                     string
}

// DistributionJob is one tracker's work for one target, carried on its own
// peer stream.
type DistributionJob struct {
	TrackerID      int64
	PeerInstanceID string
	Status         JobStatus
	Priority       int
	Started        time.Time
	Finished       time.Time
	Error          string
}

// TrackerStatus summarises job statuses: failed if any failed, in progress
// while any is running or queued, completed when all completed.
func TrackerStatus(jobs []DistributionJob) JobStatus {
	if len(jobs) == 0 {
		return JobNotDistributed
	}
	status := JobCompleted
	for _, j := range jobs {
		switch j.Status {
		case JobFailed:
			return JobFailed
		case JobAborted:
			status = JobAborted
		case JobInProgress, JobInQueue, JobNotDistributed:
			if status != JobAborted {
				status = JobInProgress
			}
		}
	}
	return status
}

// BundleRetentionConfig is the release bundle v1 retention singleton.
type BundleRetentionConfig struct {
	Automatic                    bool   `json:"automatic"`
	VersionsToKeepPerBundle      int    `json:"versions_to_keep_per_bundle"`
	MaxDaysToKeep                int    `json:"max_days_to_keep"`
	CronPattern                  string `json:"cron_pattern,omitempty"`
	MaxDeletionsPerBackgroundRun int    `json:"max_deletions_per_background_run"`
	Enabled                      bool   `json:"enabled"`
}

// BundleOperation is a retention audit operation.
type BundleOperation string

// Bundle retention operations.
const (
	BundleDelete BundleOperation = "DELETE"
	BundleKeep   BundleOperation = "KEEP"
	BundleUnkeep BundleOperation = "UNKEEP"
)

// BundleRetentionAudit records a keep mark change or a retention deletion.
type BundleRetentionAudit struct {
	BundleName    string
	BundleVersion string
	Operation     BundleOperation
	Created       time.Time
	CreatedBy     string
}

// BundleExport is an exported bundle archive.
type BundleExport struct {
	BundleName    string
	BundleVersion string
	Status        JobStatus
	ArchiveSHA256 string
	Created       time.Time
}
