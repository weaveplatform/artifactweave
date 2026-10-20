package helm

import "github.com/weaveplatform/artifactweave/internal/archive"

// Inspected reads what Inspect returned: the chart it read, nil for content
// that is none. ok is false when Inspect returned no inspection.
func Inspected(v any) (c *Chart, ok bool) {
	in, ok := v.(*inspection)
	if !ok {
		return nil, false
	}
	return in.chart, true
}

// Inspection is what Inspect would return for content sha holding c.
func Inspection(sha string, c *Chart) any { return &inspection{sha256: sha, chart: c} }

// SetLimits replaces the archive limits until the returned func restores
// them.
func SetLimits(l archive.Limits) func() {
	old := limits
	limits = l
	return func() { limits = old }
}
