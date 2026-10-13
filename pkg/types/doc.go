// Package types holds artifactweave's internal domain model (decision 0020):
// one representation per entity in docs/research/15-data-model.md, in the
// shape the services and stores work with. JFrog's several wire forms of the
// same entity (legacy camelCase, Access snake_case, the YAML descriptor) are
// converted to and from these types; the wire structs themselves live in
// pkg/types/wire and nowhere else.
//
// The repository configuration is the one entity whose wire and internal
// shapes coincide, so it stays pkg/repo.Config and is not duplicated here.
//
// JSON tags appear only on types the stores persist as JSON documents
// (research 15 section 4 lists which columns are documents): role flags,
// permission resources, webhook criteria and handlers, cleanup and archive
// criteria, the trash record, settings values, release bundle specs and
// distribution rules, federation and worker filters. Those tags are
// snake_case and belong to artifactweave's storage format, not to any JFrog
// API. Every other type is a plain Go value. Secrets carried in memory are
// tagged `json:"-"` so they can never reach a document; the stores keep them
// in sealed columns (decision 0017).
package types
