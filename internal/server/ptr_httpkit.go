package server

// Deref returns *p, or the zero value when p is nil, for the optional
// fields of generated request types.
func Deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// DerefSlice returns *p, or nil when p is nil.
func DerefSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// PtrTo returns a pointer to a copy of v, for the optional fields of
// generated response types.
func PtrTo[T any](v T) *T { return &v }
