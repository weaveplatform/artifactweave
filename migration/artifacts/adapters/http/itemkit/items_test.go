package itemkit_test

import (
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Matrix values are form-decoded as the JFrog CLI encodes them (QueryEscape)
// and Artifactory decodes them (URLDecoder): "+" is a space and "%2B" a
// plus. Keys keep "+", whose trailing use marks a mandatory property.
func TestAccMatrixParameterDecoding(t *testing.T) {
	for name, c := range map[string]struct {
		escaped   string
		expected  types.Properties
		mandatory []string
	}{
		"plus is a space":       {"libs/a.txt;build.name=my+build", types.Properties{"build.name": {"my build"}}, nil},
		"encoded plus is plus":  {"libs/a.txt;v=a%2Bb", types.Properties{"v": {"a+b"}}, nil},
		"percent-encoded space": {"libs/a.txt;v=a%20b", types.Properties{"v": {"a b"}}, nil},
		"mandatory key":         {"libs/a.txt;k+=v", types.Properties{"k": {"v"}}, []string{"k"}},
		"mandatory, plus value": {"libs/a.txt;k+=x+y", types.Properties{"k": {"x y"}}, []string{"k"}},
		"several values":        {"libs/a.txt;k=a,b", types.Properties{"k": {"a", "b"}}, nil},
		"encoded separator":     {"libs/a.txt;k=a%2Cb", types.Properties{"k": {"a", "b"}}, nil},
		"escaped comma":         {"libs/a.txt;k=a%5C%2Cb%2Cc", types.Properties{"k": {"a,b", "c"}}, nil},
		"as jf sends a build": {
			"libs/a.txt;build.name=my+build;build.number=1;build.timestamp=1760000000000",
			types.Properties{
				"build.name": {"my build"}, "build.number": {"1"}, "build.timestamp": {"1760000000000"},
			},
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := itemkit.ParseEscapedItemPath(c.escaped)
			if err != nil {
				t.Fatal(err)
			}
			if len(req.Props) != len(c.expected) {
				t.Fatalf("props = %v, want %v", req.Props, c.expected)
			}
			for k, v := range c.expected {
				if !slices.Equal(req.Props[k], v) {
					t.Errorf("%s = %q, want %q", k, req.Props[k], v)
				}
			}
			for _, k := range c.mandatory {
				if !req.Mandatory[k] {
					t.Errorf("%s is not mandatory", k)
				}
			}
			if req.Path != "a.txt" {
				t.Errorf("path = %q", req.Path)
			}
		})
	}
	if _, err := itemkit.ParseEscapedItemPath("libs/a.txt;k=%zz"); err == nil {
		t.Error("a malformed escape was accepted")
	}
}
