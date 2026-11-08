package upstream

// enabled reports a repository switch that is set and on, as core remote's
// own helper does; an unset switch is off.
func enabled(v *bool) bool { return v != nil && *v }
