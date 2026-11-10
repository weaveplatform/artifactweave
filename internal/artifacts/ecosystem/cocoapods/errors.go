package cocoapods

import (
	"errors"
	"fmt"
)

var errInvalidPackage = errors.New("cocoapods: invalid cocoapods package")

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", errInvalidPackage, reason)
}
