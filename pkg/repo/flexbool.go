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

// FlexBool is a boolean that Artifactory types inconsistently on the wire.
// JFrog's JSON documentation and OpenAPI schema declare enableFileListsIndexing,
// downloadRedirect, cdnRedirect, priorityResolution and blockPushingSchema1 as
// strings ("false") on local and federated repositories but as booleans on
// remote and virtual ones, with one exception the crawled schema shows:
// priorityResolution is a boolean on federated. Federated members[].enabled is
// always a string (docs/research/03-repository-configuration-model.md, "Typing
// traps", and docs/research/15-data-model.md section 5). The
// JFrog Terraform provider reads these back after a create and compares them,
// so the server must accept both spellings on input and echo the one
// Artifactory uses for the class on output. Config.MarshalJSON sets asString;
// a FlexBool marshalled on its own is a plain boolean.
type FlexBool struct {
	Value    bool
	asString bool
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

// MarshalJSON writes the boolean, quoted when the owning Config decided the
// class uses the string spelling.
func (f FlexBool) MarshalJSON() ([]byte, error) {
	if f.asString {
		return []byte(strconv.Quote(strconv.FormatBool(f.Value))), nil
	}
	return []byte(strconv.FormatBool(f.Value)), nil
}
