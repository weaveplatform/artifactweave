package archivecheck

import (
	"errors"
	"fmt"
)

// ErrInvalid identifies malformed or ambiguous archives that native clients
// could interpret differently from the registry's validated metadata.
var ErrInvalid = errors.New("invalid ZIP archive")

func invalid(detail string) error { return fmt.Errorf("%w: %s", ErrInvalid, detail) }
