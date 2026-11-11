package model

import "time"

// ArchivedItem is the record an archived item leaves behind: its full
// metadata, so a restore recreates it and AQL over archives can search it.
type ArchivedItem struct {
	ID             int64
	Namespace      string
	Item           Item
	Properties     Properties
	Stats          ItemStats
	PackageType    string
	PackageName    string
	PackageVersion string
	PolicyKey      string
	ExecutionID    int64
	ArchivedAt     time.Time
	ExpiresAt      time.Time
	ColdBlobSHA256 string
}

// Expired reports whether the record is due for purging.
func (a ArchivedItem) Expired(now time.Time) bool {
	return !a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt)
}
