package authz

import accessapi "github.com/weaveplatform/artifactweave/migration/access/api"

var _ accessapi.Decider = (*Decider)(nil)

// AsAPI returns d as the access/api Decider other modules ask, or a nil
// interface when d is nil. A nil *Decider stored in the interface would be a
// typed nil, which compares unequal to nil: a caller testing for an absent
// decider would then call through a nil pointer.
func AsAPI(d *Decider) accessapi.Decider {
	if d == nil {
		return nil
	}
	return d
}

// FromAPI returns the evaluator behind d, for the questions only it answers
// (Holds, Grantable, Self, Actions, RepoConfig and ManagesResources), and
// true when d holds a non-nil *Decider. A nil d, a typed nil and any other
// implementation, such as a test fake, give nil and false.
func FromAPI(d accessapi.Decider) (*Decider, bool) {
	ad, ok := d.(*Decider)
	return ad, ok && ad != nil
}
