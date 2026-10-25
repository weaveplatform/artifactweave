package cocoapods

import (
	"errors"
	"fmt"
)

var errInvalidPackage = errors.New("invalid CocoaPods package")

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", errInvalidPackage, reason)
}
