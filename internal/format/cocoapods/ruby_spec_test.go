package cocoapods

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnitStaticRubyCommonParameterForms(t *testing.T) {
	data := []byte(`Pod::Spec.new do |s|
  s.name = 'HelloPod'
  s.version = '1.0.0'
  s.author = 'Example'
  s.platform = :ios, '13.0'
  s.osx.deployment_target = '11.0'
  s.ios.source_files = ['Sources/**/*.h', 'Sources/**/*.m',]
  s.frameworks = ['Foundation', 'CoreGraphics']
  s.requires_arc = true
  s.license = { type: 'MIT', file: 'LICENSE' }
  s.dependency 'FoundationKit'
  s.test_spec 'Tests' do |ts|
    ts.source_files = 'Tests/**/*.m'
    ts.dependency 'Quick', '~> 7.0'
  end
  s.app_spec 'Demo' do |app|
    app.source_files = 'Demo/**/*.m'
  end
end`)
	encoded, err := staticRubySpec(data)
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(encoded, &spec); err != nil {
		t.Fatal(err)
	}
	if spec["authors"] != "Example" || spec["requires_arc"] != true ||
		spec["license"].(map[string]any)["type"] != "MIT" ||
		spec["platforms"].(map[string]any)["ios"] != "13.0" ||
		spec["platforms"].(map[string]any)["osx"] != "11.0" ||
		spec["ios"].(map[string]any)["source_files"].([]any)[1] != "Sources/**/*.m" ||
		spec["test_specs"].([]any)[0].(map[string]any)["name"] != "Tests" ||
		spec["app_specs"].([]any)[0].(map[string]any)["name"] != "Demo" {
		t.Fatalf("static CocoaPods parameter conversion: %+v", spec)
	}
	if got := spec["dependencies"].(map[string]any)["FoundationKit"].([]any); len(got) != 0 {
		t.Fatalf("unconstrained dependency changed: %v", got)
	}
}

func TestUnitStaticRubyRejectsUnsafeAndAmbiguousStatements(t *testing.T) {
	base := "Pod::Spec.new do |s|\n  s.name = 'HelloPod'\n  %s\nend"
	for _, tc := range []struct{ statement, reason string }{
		{"s.subspec '../escape' do |ss|\n  end", "invalid static subspec name"},
		{"other.summary = 'wrong block'", "podspec assignment uses another block variable"},
		{"s.platform = :windows, '10.0'", "invalid static platform"},
		{"s.platform = :ios, true", "invalid static platform version"},
		{"s.windows.source_files = 'Sources/*'", "invalid static platform field"},
		{"s.ios.deployment_target = true", "invalid static deployment version"},
		{"other.dependency 'Quick'", "podspec dependency uses another block variable"},
		{"s.dependency 17", "nonliteral Ruby expression"},
		{"s.dependency 'Quick', true", "invalid static dependency requirement"},
		{"s.dependency", "unsupported Ruby podspec statement"},
		{"s.source = { :http => ENV['URL'] }", "nonliteral Ruby expression"},
		{"s.summary = 'quoted'; system('whoami')", "dynamic Ruby expression"},
		{"s.summary = ['open'", "unterminated Ruby literal continuation"},
		{"s.summary = 'unterminated", "unterminated Ruby string"},
		{"system('whoami')", "unsupported Ruby podspec statement"},
	} {
		t.Run(tc.statement, func(t *testing.T) {
			_, err := staticRubySpec([]byte(strings.ReplaceAll(base, "%s", tc.statement)))
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("unsafe static podspec statement: %v, want %q", err, tc.reason)
			}
		})
	}
	for _, tc := range []struct{ podspec, reason string }{
		{"s.name = 'HelloPod'", "unsupported Ruby podspec statement"},
		{"Pod::Spec.new do |s|\n  s.name = 'HelloPod'", "incomplete Ruby podspec"},
		{"Pod::Spec.new do |s|\nend\ns.name = 'HelloPod'", "unsupported Ruby podspec statement"},
		{"Pod::Spec.new do |s|\n  s.subspec 'Core' do |ss|\n    s.source_files = 'Sources/*'\n  end\nend", "podspec assignment uses another block variable"},
	} {
		_, err := staticRubySpec([]byte(tc.podspec))
		if err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("incomplete or escaped podspec: %v, want %q", err, tc.reason)
		}
	}
}

func TestUnitRubyLiteralDataOnlyBoundary(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  any
	}{
		{`"Hello\nPod"`, "Hello\nPod"},
		{`'don\'t'`, "don't"},
		{`:ios`, "ios"},
		{`false`, false},
		{`[]`, []any{}},
		{`{}`, map[string]any{}},
		{`['A', { :kind => 'B' },]`, []any{"A", map[string]any{"kind": "B"}}},
	} {
		got, err := rubyLiteral(tc.value)
		if err != nil {
			t.Errorf("literal %q: %v", tc.value, err)
			continue
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(tc.want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("literal %q = %s, want %s", tc.value, gotJSON, wantJSON)
		}
	}
	for _, tc := range []struct{ value, reason string }{
		{"", "empty Ruby literal"},
		{"'broken", "invalid quoted literal"},
		{`"\q"`, "decode quoted podspec literal"},
		{`'bad\q'`, "unsupported Ruby string escape"},
		{`:bad-symbol`, "invalid Ruby symbol"},
		{`['ok', ENV['HOME']]`, "nonliteral Ruby expression"},
		{`{ not_a_pair }`, "invalid Ruby hash entry"},
		{`{ [] => 'value' }`, "invalid Ruby hash key"},
		{`{ :kind => ENV['HOME'] }`, "nonliteral Ruby expression"},
		{`['A',, 'B']`, "empty Ruby literal"},
	} {
		_, err := rubyLiteral(tc.value)
		if err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Errorf("Ruby literal %q: %v, want %q", tc.value, err, tc.reason)
		}
	}
}
