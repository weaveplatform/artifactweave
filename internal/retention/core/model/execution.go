package model

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Validate checks identities before private state becomes recoverable work.
// A credential reference is not permission; the worker refreshes it on use.
func (e CleanupExecution) Validate() error {
	if e.RunID <= 0 || e.Policy.Key == "" || e.Policy.Revision == "" || e.Policy.ItemType != CleanupPackage || e.At.IsZero() ||
		e.Policy.ProjectKey != "" && e.ProjectIncarnation == "" ||
		len(e.Occurrence) > 128 || !utf8.ValidString(e.Occurrence) || strings.ContainsRune(e.Occurrence, 0) {
		return fmt.Errorf("invalid cleanup execution snapshot: %w", kernel.ErrInvalid)
	}
	return ValidateCleanupCredential(e.Credential)
}

// ValidateCleanupCredential rejects name-only and registry capabilities. The
// reference retains the original password revision or stored token identity;
// current authentication and permission checks still precede every effect.
func ValidateCleanupCredential(c kernel.CredentialReference) error {
	if c.Name == "" || c.Anonymous || c.Registry != nil || !c.Password && c.TokenID == "" {
		return fmt.Errorf("cleanup requires an original authenticated credential: %w", kernel.ErrBadCredentials)
	}
	return nil
}
