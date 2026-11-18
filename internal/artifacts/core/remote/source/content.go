package source

// ContentOpenError identifies a failure opening an already-selected cached
// representation. Its cause retains the normal source answer. A metadata merger
// can omit this content failure without treating lookup or authority errors as
// missing content; other consumers keep the wrapped error's classification.
type ContentOpenError struct{ Err error }

func (e *ContentOpenError) Error() string { return e.Err.Error() }
func (e *ContentOpenError) Unwrap() error { return e.Err }
