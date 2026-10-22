package layout

// RemotePath finds the unique upstream file whose lossless canonical projection
// is path. A remote namespace contains only that projection: for example a
// Maven POM projects to Gradle's ivy descriptor, so the other Gradle artifact
// spelling ending in .pom cannot become an alias for the same upstream bytes.
// Copy/move Translate retains its stricter, symmetric source-namespace rules.
func RemotePath(canonical, upstream *Layout, path string) (string, error) {
	if _, err := canonical.Match(path); err != nil {
		return "", err
	}
	var matched string
	for _, candidate := range upstream.candidates(canonical, path) {
		projected, err := Translate(upstream, canonical, candidate)
		if err != nil || projected != path {
			continue
		}
		if matched != "" && matched != candidate {
			return "", &LossError{
				From:   canonical.def.Name,
				To:     upstream.def.Name,
				Path:   path,
				Target: matched,
				Rival:  candidate,
			}
		}
		matched = candidate
	}
	if matched == "" {
		return "", &LossError{From: canonical.def.Name, To: upstream.def.Name, Path: path}
	}
	return matched, nil
}
