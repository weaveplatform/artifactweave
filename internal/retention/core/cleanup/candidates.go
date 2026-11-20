package cleanup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Reason explains an internal selection decision. These values are not vendor
// report fields; report serialization remains with CLN-05.
type Reason string

const (
	ReasonSelected            Reason = "selected"
	ReasonProtected           Reason = "protected_version"
	ReasonPackageExcluded     Reason = "excluded_package"
	ReasonPackageNotIncluded  Reason = "package_not_included"
	ReasonVersionExcluded     Reason = "excluded_version"
	ReasonVersionNotIncluded  Reason = "version_not_included"
	ReasonPropertyExcluded    Reason = "excluded_property"
	ReasonPropertyNotIncluded Reason = "property_not_included"
	ReasonCreated             Reason = "creation_age"
	ReasonDownloaded          Reason = "download_age"
	ReasonNoLead              Reason = "missing_lead"
	ReasonLeadUnavailable     Reason = "lead_contract_unavailable"
	ReasonNoFiles             Reason = "no_indexed_files"
	ReasonChanged             Reason = "snapshot_changed"
)

// CandidatePage is bounded by the requested item page, with actual content and
// mutable selection facts in every item. PolicyRevision/At name the frozen rule,
// and LeadFingerprint binds its format-owned lead evidence. None authorizes a
// delete: execution must refresh authority, protection, criteria and item facts.
type CandidatePage struct {
	PolicyKey, PolicyRevision string
	At                        time.Time
	Version                   artifactsapi.IndexVersion
	Reason                    Reason
	LeadFingerprint           string
	Items                     []artifactsapi.IndexFile
	Next                      string
}

func (s *Selection) needsLead() bool {
	c := s.snapshot.Policy.Criteria
	return c.CreatedBeforeInDays != 0 || c.CreatedBeforeInMonths != 0 || c.LastDownloadedBeforeInDays != 0 || c.LastDownloadedBeforeInMonths != 0 ||
		len(c.IncludedProperties) > 0 ||
		len(c.ExcludedProperties) > 0
}

func versionIdentity(v artifactsapi.IndexVersion) versionFacts {
	return versionFacts{Package: v.Package.Name, Version: v.Version, Protected: v.Protected}
}

// Candidates evaluates one current indexed version and returns only one bounded
// file page. Lead scans retain constant-sized state and use bounded index pages,
// refreshing current authority for each read. They never read blob contents or
// hold a metadata transaction across the complete version traversal.
func (s *Selection) Candidates(ctx context.Context, ref artifactsapi.IndexVersion, page kernel.Page) (CandidatePage, error) {
	page, err := page.Normalize()
	if err != nil {
		return CandidatePage{}, err
	}
	current, err := s.currentVersion(ctx, ref)
	if err != nil {
		return CandidatePage{}, err
	}
	out := CandidatePage{
		PolicyKey:      s.snapshot.Policy.Key,
		PolicyRevision: s.snapshot.Policy.Revision,
		At:             s.snapshot.At,
		Version:        current,
		Reason:         s.criteria.identityReason(versionIdentity(current)),
	}
	if out.Reason != ReasonSelected {
		return out, nil
	}
	var lead leadResult
	if s.needsLead() {
		lead, err = s.leads(ctx, current)
		if err != nil {
			return CandidatePage{}, err
		}
		out.Reason, out.LeadFingerprint = lead.reason, lead.fingerprint
		if out.Reason != ReasonSelected {
			return out, nil
		}
	}
	items, next, err := s.files(ctx, current, page)
	if err != nil {
		return CandidatePage{}, err
	}
	if len(items) == 0 {
		out.Reason = ReasonNoFiles
		return out, nil
	}
	if s.needsLead() {
		confirmed, err := s.leads(ctx, current)
		if err != nil {
			return CandidatePage{}, err
		}
		if confirmed.fingerprint != lead.fingerprint || confirmed.reason != lead.reason {
			out.Reason = ReasonChanged
			return out, nil
		}
	}
	latest, err := s.currentVersion(ctx, current)
	if err != nil {
		return CandidatePage{}, err
	}
	if latest.Protected {
		out.Version, out.Reason = latest, ReasonProtected
		return out, nil
	}
	out.Items, out.Next = items, next
	return out, nil
}

type leadResult struct {
	reason      Reason
	fingerprint string
}

type leadScan struct {
	found, known bool
	single       bool
	best         artifactsapi.IndexFile
	reason       Reason
	hash         hash.Hash
}

// leadEvidence excludes traversal rank and descriptive parent copies, retaining
// exactly the item's incarnation/content/mutable criteria used in this decision.
func leadEvidence(file artifactsapi.IndexFile) any {
	return struct {
		ID                                                             int64
		Path                                                           string
		Size                                                           int64
		Actual                                                         kernel.Checksums
		Created, Modified, Updated, PropertiesModified, LastDownloaded time.Time
		Properties                                                     map[string][]string
	}{file.ID, file.Path, file.Size, file.Actual, file.Created, file.Modified, file.Updated, file.PropertiesModified, file.LastDownloaded, file.Properties}
}

func (s *Selection) leadReason(file artifactsapi.IndexFile) Reason {
	v := versionIdentity(file.Version)
	v.Created, v.LastDownloaded, v.LeadProperties = file.Created, file.LastDownloaded, file.Properties
	return s.criteria.reason(v)
}

func (s *Selection) leads(ctx context.Context, ref artifactsapi.IndexVersion) (leadResult, error) {
	state := leadScan{reason: ReasonSelected, hash: sha256.New()}
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		rows, next, err := s.files(ctx, ref, page)
		if err != nil {
			return leadResult{}, err
		}
		for _, file := range rows {
			if file.Lead == nil {
				continue
			}
			state.known = true
			if !file.Lead.Found {
				continue
			}
			if state.found && state.single != file.Lead.Single {
				return leadResult{}, fmt.Errorf("mixed cleanup lead contract: %w", kernel.ErrStore)
			}
			state.single = file.Lead.Single
			if state.single {
				if !state.found || file.Lead.Rank > state.best.Lead.Rank || file.Lead.Rank == state.best.Lead.Rank && file.Lead.TieBreak > state.best.Lead.TieBreak {
					state.best = kernel.Clone(file)
				}
			} else {
				// One indexed version can contain several admitted archive leads
				// (for example Debian architectures). Keep the version if any
				// lead fails criteria; never choose an arbitrary architecture.
				if reason := s.leadReason(file); reason != ReasonSelected && state.reason == ReasonSelected {
					state.reason = reason
				}
				if err := json.NewEncoder(state.hash).Encode(leadEvidence(file)); err != nil {
					return leadResult{}, fmt.Errorf("fingerprint cleanup lead: %w", err)
				}
			}
			state.found = true
		}
		if next == "" {
			break
		}
		page.After = next
	}
	if !state.known {
		return leadResult{reason: ReasonLeadUnavailable}, nil
	}
	if !state.found {
		return leadResult{reason: ReasonNoLead}, nil
	}
	if state.single {
		state.reason = s.leadReason(state.best)
		if err := json.NewEncoder(state.hash).Encode(leadEvidence(state.best)); err != nil {
			return leadResult{}, fmt.Errorf("fingerprint cleanup lead: %w", err)
		}
	}
	return leadResult{reason: state.reason, fingerprint: hex.EncodeToString(state.hash.Sum(nil))}, nil
}
