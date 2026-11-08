package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
)

// RepoFilter narrows a repository listing; empty fields match everything. Its
// fields are the store's repository filter's, in the same order and of the
// same types, so the artifacts module converts one into the other with a
// plain conversion, which stops compiling if either changes.
type RepoFilter struct {
	Class       repo.Class
	PackageType repo.PackageType
	ProjectKey  string
}

// ErrChanged is a repository delete that found the repository's content
// changing under it: an item it moved to the trash can was gone by the time
// it deleted the content. Nothing is deleted, and a retry sees the content as
// it now is. Repositories.Remove returns it wrapped, so callers match it with
// errors.Is. The text is logged, so it stays byte-identical.
var ErrChanged = errors.New("repos: the repository's content changed during the delete")

// MsgKeyExists is the message of the 400 answer to a create whose key is
// already taken, compared without regard to case. It is JFrog's text, which
// clients match, so it stays byte-identical; the repositories API also
// reports it per entry of a batch create.
const MsgKeyExists = "Case insensitive repository key already exists"

// IsNull reports whether raw, a JSON value from an update body or a
// configuration descriptor, is null, ignoring surrounding white space. A null
// resets a field to its default on an update (Repositories.Merge) and deletes
// an entry in the descriptor (decision 0009), so callers that build a patch
// test each value with it.
func IsNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// Repositories reads, validates and writes repository configurations for the
// modules that change repositories without owning them: the configuration
// descriptor, and access's key pair, role and permission services. The
// artifacts module implements it over its store and the access contracts it
// is built with, so a caller needs neither.
//
// Every method that takes a ctx reads and writes with it: called inside a
// store transaction, it takes part in that transaction and commits or rolls
// back with it (decision 0009: the descriptor applies its deletes, writes and
// reindex requests in one transaction). The methods without a ctx read and
// write nothing. No method wraps the error of the store call or repository
// operation behind it, so a caller's own wrapping stays as it was and
// errors.Is finds the store's sentinels (kernel.ErrNotFound,
// kernel.ErrConflict) and ErrChanged. The validation methods answer a 400
// apierror (platform/httpkit/apierror) whose message the repositories API,
// the descriptor and config apply all show unchanged, so every entry point
// refuses a bad configuration with the same text.
type Repositories interface {
	// Get returns the configuration stored under key, which must match
	// exactly, including remote passwords and sensitive header values: a
	// caller that shows it masks them first (repo.Masked).
	// kernel.ErrNotFound when no repository has the key.
	Get(ctx context.Context, key string) (repo.Config, error)
	// List returns every repository that f matches, ordered by key. It is
	// unpaged, as Artifactory lists repositories.
	List(ctx context.Context, f RepoFilter) ([]repo.Config, error)
	// UsingKeypair returns the keys of the repositories whose primary or
	// secondary key pair reference is name, ordered.
	UsingKeypair(ctx context.Context, name string) ([]string, error)
	// Create stores c as it is: the caller applies PrepareCreate and the
	// checks first. kernel.ErrConflict when the key exists.
	Create(ctx context.Context, c repo.Config) error
	// Update replaces the configuration stored under c.Key with c as it is:
	// the caller merges and checks it first (Merge). kernel.ErrNotFound when
	// no repository has the key.
	Update(ctx context.Context, c repo.Config) error
	// Remove deletes the repository key with its content: its items and, for
	// a remote repository, its cache, the root records that hold their own
	// properties, its package index, its place in the virtual repositories
	// that list it, and its configuration. by is who deletes it. It returns
	// the number of files deleted and whether a configuration existed; with
	// none, found is false and the error nil.
	//
	// While the trash can is on, the repository's own content moves to it
	// under key, as a delete of its root does, and a remote repository's
	// cache is released for garbage collection; while it is off, all of it
	// is released.
	//
	// The delete is one store transaction, however large the repository, so
	// a failure part of the way leaves the repository, its content and the
	// trash can as they were. Inside a transaction that ctx carries, Remove
	// joins it and the caller's commit or rollback decides for both. The SQL
	// store fails the caller's transaction when Remove fails even if the
	// caller drops the error, and the in-memory store does not, so callers
	// return it. Errors name the step that failed, wrapped as "delete
	// repository <key>: %w", or "get repository: %w" when reading the
	// configuration fails; ErrChanged, wrapped, refuses a delete whose
	// content changed under it.
	Remove(ctx context.Context, key, by string) (int64, bool, error)
	// RequestReindex queues a reindex of the repository key on the
	// persistent work queue (format.KindReindex in artifacts/core/format),
	// due at at, with by recorded as who asked for it: the payload is
	// {"by":"<by>"}, or {} when by is empty, byte for byte what the
	// configuration descriptor has always queued. Inside a transaction that
	// ctx carries, the request commits or rolls back with it. Requests made
	// for one key before its run begins coalesce into that run, and the
	// queue's workers poll the store, so the reindex runs whichever process
	// asked for it. Errors are the queue's: kernel.ErrInvalid for an empty or
	// overlong key.
	RequestReindex(ctx context.Context, key, by string, at time.Time) error
	// Protected reports whether key names a repository that cannot be
	// deleted, compared without regard to case: a blank key, Artifactory's
	// trash can (auto-trashcan) and support bundle (jfrog-support-bundle)
	// repositories, and the built-in build-info repository
	// (artifactory-build-info), which the repositories API cannot create
	// again.
	Protected(key string) bool
	// PrepareCreate applies the defaults Artifactory applies on create
	// (repo.Defaults) to c, validates it (repo.Validate), and refuses a key
	// that existing, the stored repositories and those created earlier in
	// the same batch, already holds in any case. It returns c with its
	// defaults, and a 400 apierror with the validation message or
	// MsgKeyExists.
	PrepareCreate(c repo.Config, existing []repo.Config) (repo.Config, error)
	// CheckMembers refuses a virtual repository whose members, its default
	// deployment repository among them, do not exist among known, or have a
	// package type the virtual repository cannot aggregate
	// (repo.AcceptsMember); a P2 virtual repository's members of another
	// package type must be local. Other classes pass. The refusal is a 400
	// apierror naming the member.
	CheckMembers(c repo.Config, known []repo.Config) error
	// CheckKeypairs refuses signing key references (primaryKeyPairRef,
	// secondaryKeyPairRef, and keyPair on an Alpine or Debian virtual
	// repository) to key pairs that do not exist, or whose type the
	// repository cannot use (repo.ValidateKeyPair), because the repository
	// would otherwise fail only when it first signs or verifies. It reads
	// each named pair through access with ctx. A missing pair answers a 400
	// apierror "Key pair '<name>' does not exist" and an unusable one a 400
	// apierror with repo.ValidateKeyPair's message; an error reading a pair
	// is wrapped as "repos: key pair <name>: %w".
	CheckKeypairs(ctx context.Context, c repo.Config) error
	// CheckEnvironments applies Artifactory's rule for a repository's
	// environments (decision 0013) to c in place. Outside a project they are
	// cleared, so none are stored. In a project an empty list becomes DEV,
	// and every entry must be DEV, PROD, a global environment or one of the
	// project's own, named "<projectKey>-<name>"; names compare without
	// regard to case and are rewritten in the environment's own spelling,
	// once each. It reads the environment names through access with ctx, and
	// a project that does not exist has only the built-in and global
	// environments. An unknown name answers a 400 apierror "Environment
	// '<name>' does not exist"; an error reading the names is wrapped as
	// "repos: global environments: %w" or "repos: environments of project
	// <key>: %w".
	CheckEnvironments(ctx context.Context, c *repo.Config) error
	// Merge merges an update body, patch, into stored with Artifactory's
	// rules and returns the result, validated (repo.ValidateStored and
	// repo.ValidateUpdate) with its defaults applied, and with a sensitive
	// header's stored value kept where the body sent that header back
	// empty, as repo.Masked shows it (repo.RestoreSecrets). A field present
	// replaces the stored value, a field left out keeps it, and null
	// (IsNull) resets it to its default. The key, class and package type
	// cannot change, nor can a field the package type takes only on create;
	// a key respelt in another case is a change, while a class or package
	// type respelt names the stored one, which keeps its spelling. Merge
	// consumes patch, so a caller that needs it afterwards passes a copy. A
	// refusal is a 400 apierror with Artifactory's message.
	Merge(stored repo.Config, patch map[string]json.RawMessage) (repo.Config, error)
	// OwesReindex reports whether an update of stored to updated owes the
	// repository a reindex (RequestReindex): one that gives a Terraform
	// repository the terraformType it lacked, so the files stored before
	// are indexed as that type says (decision 0040).
	OwesReindex(stored, updated repo.Config) bool
}
