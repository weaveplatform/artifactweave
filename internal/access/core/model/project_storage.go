package model

// ProjectStorage is private logical accounting for one project lifetime. It
// counts live owned content before CAS deduplication; shares never copy it.
// An absent record needs initialization from current artifact metadata under
// the project guard, which is distinct from an initialized empty project.
type ProjectStorage struct {
	Bytes int64
	// WarningKey names one 75% notification occurrence across durable retries.
	// Clearing it rearms a later crossing. Delivery must also check the current
	// project incarnation, so deletion/recreation cannot revive queued mail.
	WarningKey string
}

// ProjectStorageWarning is a self-contained durable handoff. A mail worker
// verifies Incarnation and WarningKey against current state before sending;
// retry must keep this occurrence's identity rather than create a new message.
type ProjectStorageWarning struct {
	ProjectKey  string             `json:"project_key"`
	Incarnation ProjectIncarnation `json:"incarnation"`
	WarningKey  string             `json:"warning_key"`
	Bytes       int64              `json:"bytes"`
	Limit       int64              `json:"limit"`
}
