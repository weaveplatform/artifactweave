package layout

import artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"

var _ artifactsapi.Layouts = Checker{}

// Checker implements artifacts/api.Layouts with Compile, so configuration
// refuses exactly the layout definitions the engine cannot compile. The zero
// value is ready to use; it holds no state and is safe for concurrent use.
type Checker struct{}

// Check compiles d with Compile and returns Compile's error as it is, nil
// when d compiles. The conversion to Definition relies on LayoutDefinition
// having Definition's fields in the same order and of the same types, so it
// stops compiling if either struct changes rather than dropping a field.
func (Checker) Check(d artifactsapi.LayoutDefinition) error {
	_, err := Compile(Definition(d))
	return err
}
