package api

import (
	"context"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PasswordEncryption is the password encryption policy: which kind of
// password a Basic credential may carry (Access's
// security.authentication.password-encryption, the descriptor's
// security.passwordSettings.encryptionPolicy).
type PasswordEncryption string

// The policies, spelled as 7.x's EncryptionPolicy XML values.
const (
	// EncryptionSupported accepts the clear-text password and the encrypted
	// password alike. It is the default.
	EncryptionSupported PasswordEncryption = "supported"
	// EncryptionRequired refuses a clear-text password outside a UI
	// request: only the encrypted password authenticates.
	EncryptionRequired PasswordEncryption = "required"
	// EncryptionUnsupported refuses the encrypted password.
	EncryptionUnsupported PasswordEncryption = "unsupported"
)

// SecurityPolicy is the baseline security policy the descriptor's security
// section sets beside anonymous access and LDAP: the user lock policy, the
// password expiration policy and the password encryption policy. The
// Artifactory and Access REST APIs write the same state, so one value
// describes every surface.
type SecurityPolicy struct {
	// LockEnabled and LoginAttempts are the user lock policy
	// (security.userLockPolicy).
	LockEnabled   bool
	LoginAttempts int
	// ExpirationEnabled, PasswordMaxAge (days) and NotifyByEmail are the
	// password expiration policy (security.passwordSettings.expirationPolicy).
	ExpirationEnabled bool
	PasswordMaxAge    int
	NotifyByEmail     bool
	// Encryption is security.passwordSettings.encryptionPolicy.
	Encryption PasswordEncryption
}

// SecurityPolicyInvalid refuses a policy. Section is the descriptor's path
// below security ("userLockPolicy", "passwordSettings.expirationPolicy",
// "passwordSettings") and Reason the rule the REST APIs word the same way. It
// matches kernel.ErrInvalid.
type SecurityPolicyInvalid struct {
	Section string
	Reason  string
}

func (e *SecurityPolicyInvalid) Error() string { return "security." + e.Section + ": " + e.Reason }

// Unwrap makes a refusal match kernel.ErrInvalid.
func (e *SecurityPolicyInvalid) Unwrap() error { return kernel.ErrInvalid }

// SecurityPolicies reads and writes the baseline security policy. Every call
// uses ctx for its store, so a call inside a store transaction takes part in
// it.
type SecurityPolicies interface {
	// Current returns the effective policy: what is stored, or the default
	// of a policy never written.
	Current(ctx context.Context) (SecurityPolicy, error)
	// Check validates p with the rules the REST APIs apply and returns it as
	// it would be stored, without writing anything. A refusal matches
	// kernel.ErrInvalid and names the descriptor's field.
	Check(p SecurityPolicy) (SecurityPolicy, error)
	// Set stores p, which Check accepted. Turning the lock policy off clears
	// every lock, as the REST APIs do.
	Set(ctx context.Context, p SecurityPolicy) error
	// Forget drops what this instance's authenticator caches of the policies
	// and of the internal password switch, so a committed change applies to
	// its next login rather than after the cache's lifetime.
	Forget()
}
