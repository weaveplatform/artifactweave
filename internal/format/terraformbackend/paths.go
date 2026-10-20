package terraformbackend

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The layout of a repository (decision 0041, "Storage"): one folder per
// workspace under Root, holding the workspace record, the current state and
// each earlier state, named by its state version's epoch milliseconds.
const (
	// Root is the folder that holds every workspace.
	Root = "workspaces"
	// RecordName is the workspace record. The workspace exists while it
	// does.
	RecordName = "workspace.json"
	// LatestName is the current state.
	LatestName = "state.latest.json"
	// PropLockedBy and PropLockedAt are the lock, on the workspace folder:
	// the user holding it and when they took it, in epoch milliseconds.
	PropLockedBy = "locked.by"
	PropLockedAt = "locked.at"
	// Purpose is the purpose a state is sealed under (crypt.Keyring.Seal).
	Purpose = "terraform-state"

	statePrefix = "state."
	stateSuffix = ".json"
	// idSeparator joins the parts of a workspace and a state version ID, as
	// 7.x's "<repository key>__<workspace name>" does (RTFACT-30122).
	idSeparator = "__"
)

// ErrInvalidName is a workspace name that cannot name a folder of its own.
var ErrInvalidName = errors.New("terraformbackend: invalid workspace name")

// CheckName refuses a workspace name that is not one path segment go-tfe
// would send: empty, "." or "..", or holding a slash, white space, or a
// character no item name may hold (types.SplitItemPath). TFE's narrower
// rule (letters, digits, "-" and "_") is not applied, so a name that works
// on 7.x keeps working (decision 0041, "Identifiers").
func CheckName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\\x00") || strings.IndexFunc(name, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

// Folder is the path of workspace ws's folder.
func Folder(ws string) string { return Root + "/" + ws }

// RecordPath is the path of workspace ws's record.
func RecordPath(ws string) string { return Folder(ws) + "/" + RecordName }

// LatestPath is the path of workspace ws's current state.
func LatestPath(ws string) string { return Folder(ws) + "/" + LatestName }

// StatePath is the path of workspace ws's earlier state of version ms.
func StatePath(ws string, ms int64) string {
	return Folder(ws) + "/" + statePrefix + strconv.FormatInt(ms, 10) + stateSuffix
}

// Row is the row a state of workspace ws in repository key is sealed for,
// so a sealed state opens only in its own workspace's folder, under either
// of its names (decision 0041, "encryptStates").
func Row(key, ws string) string { return key + "/" + Folder(ws) }

// File is a state file's place in a repository.
type File struct {
	Workspace string
	// Latest marks the current state, state.latest.json.
	Latest bool
	// Ms names the state version: an earlier state's name gives it, and the
	// current state's creation time does (decision 0041, "The package
	// index"), since the state write creates it at that instant.
	Ms int64
}

// FileOf reads it as a state file; ok is false for a folder and for any
// other path, the workspace record included.
func FileOf(it types.Item) (File, bool) {
	if it.Folder {
		return File{}, false
	}
	f, ok := ParseFile(it.FullPath())
	if ok && f.Latest {
		f.Ms = it.Created.UnixMilli()
	}
	return f, ok
}

// ParseFile reads a repository path as a state file. The current state's
// Ms is the item's creation time, which the path does not give: it is zero
// here, and FileOf fills it in. An earlier state's milliseconds are
// canonical decimal, so two names never give one version.
func ParseFile(p string) (File, bool) {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) != 3 || segs[0] != Root || segs[1] == "" {
		return File{}, false
	}
	if segs[2] == LatestName {
		return File{Workspace: segs[1], Latest: true}, true
	}
	digits, prefixed := strings.CutPrefix(segs[2], statePrefix)
	digits, suffixed := strings.CutSuffix(digits, stateSuffix)
	if !prefixed || !suffixed {
		return File{}, false
	}
	ms, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || ms <= 0 || strconv.FormatInt(ms, 10) != digits {
		return File{}, false
	}
	return File{Workspace: segs[1], Ms: ms}, true
}

// Version is the package index's name of the state version ms.
func Version(ms int64) string { return strconv.FormatInt(ms, 10) }

// Created is when the state version ms was created.
func Created(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// WorkspaceID is workspace ws's ID in repository key: "<key>__<ws>".
func WorkspaceID(key, ws string) string { return key + idSeparator + ws }

// StateID is the ID of workspace ws's state version ms: the workspace's ID
// and "__<ms>".
func StateID(key, ws string, ms int64) string {
	return WorkspaceID(key, ws) + idSeparator + Version(ms)
}

// Splits lists the ways id reads as "<repository key>__<workspace name>",
// splitting at each "__" from the left. A workspace name may hold "__"
// itself, so the caller takes the first split whose repository holds the
// workspace (decision 0041, "Identifiers").
func Splits(id string) [][2]string {
	var out [][2]string
	for i := 0; i+len(idSeparator) <= len(id); i++ {
		if strings.HasPrefix(id[i:], idSeparator) && i > 0 && i+len(idSeparator) < len(id) {
			out = append(out, [2]string{id[:i], id[i+len(idSeparator):]})
		}
	}
	return out
}

// SplitStateID reads id as "<workspace ID>__<ms>". The milliseconds hold no
// "_", so the last "__" ends the workspace's ID however many its name
// holds.
func SplitStateID(id string) (workspaceID string, ms int64, ok bool) {
	i := strings.LastIndex(id, idSeparator)
	if i <= 0 {
		return "", 0, false
	}
	digits := id[i+len(idSeparator):]
	ms, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || ms <= 0 || strconv.FormatInt(ms, 10) != digits {
		return "", 0, false
	}
	return id[:i], ms, true
}
