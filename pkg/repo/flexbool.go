package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// ErrNotBoolean reports a flexible boolean that is neither a JSON boolean nor
// the strings "true" or "false".
var ErrNotBoolean = errors.New("value is not a boolean")

// FlexBool is a boolean that JFrog documents inconsistently. JFrog's JSON
// documentation and OpenAPI schema declare enableFileListsIndexing,
// downloadRedirect, cdnRedirect, priorityResolution and blockPushingSchema1 as
// strings on local and federated repositories, and federated members[].enabled
// as a string (docs/research/03-repository-configuration-model.md, "Typing
// traps"). Artifactory itself
// sends JSON booleans: the JFrog Terraform provider 12.11.14 decodes all six
// into Go bools on every class and fails its refresh on a quoted value, and its
// sample local repository shows "cdnRedirect": false. So the server writes
// booleans and accepts both spellings on input, for clients that followed the
// documentation.
type FlexBool struct {
	Value bool
}

// Bool returns the value, or false when the pointer is nil.
func (f *FlexBool) Bool() bool {
	return f != nil && f.Value
}

// Flex is a convenience constructor for literals in configuration code.
func Flex(v bool) *FlexBool {
	return &FlexBool{Value: v}
}

// UnmarshalJSON accepts true, false, "true" and "false". Anything else is an
// error rather than a silent false, because a misspelt flag on a repository
// that controls redirects or indexing must not pass validation unnoticed.
func (f *FlexBool) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("flexible boolean: %w", err)
	}
	switch v := raw.(type) {
	case bool:
		f.Value = v
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("flexible boolean %q: %w", v, ErrNotBoolean)
		}
		f.Value = parsed
	default:
		return fmt.Errorf("flexible boolean of JSON type %T: %w", raw, ErrNotBoolean)
	}
	return nil
}

// MarshalJSON writes a JSON boolean, never the documented string spelling.
func (f FlexBool) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatBool(f.Value)), nil
}
