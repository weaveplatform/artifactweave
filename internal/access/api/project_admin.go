package api

// ProjectOperation separates project administration from resource actions and
// from the additional privilege required to manage project membership.
type ProjectOperation uint8

// Project administration operations.
const (
	ProjectManage ProjectOperation = iota + 1
	ProjectManageMembers
)
