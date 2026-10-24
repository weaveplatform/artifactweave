package hex

import (
	"strings"
	"testing"
)

func TestMetadataTermParserAcceptsDocumentedPrimitiveForms(t *testing.T) {
	data := []byte(`% Hex's Erlang term file may include comments and whitespace
{<<"name">>,<<"quoted \" value">>}.
{<<"active">>,true}.
{<<"inactive">>,false}.
{<<"number">>,-12}.
{<<"list">>,[<<"one">>,{<<"nested">>,<<"two">>}]}.
`)
	fields, err := parseMetadata(data)
	if err != nil || fields["name"] != `quoted " value` || fields["number"] != int64(-12) ||
		fields["active"] != true || fields["inactive"] != false {
		t.Fatalf("primitive forms: %#v %v", fields, err)
	}
}

func TestMetadataTermParserRejectsMalformedOrUnboundedInput(t *testing.T) {
	deep := strings.Repeat("[", 26) + `<<"x">>` + strings.Repeat("]", 26)
	for _, tc := range []struct {
		name, source string
	}{
		{"empty", ""},
		{"oversized", strings.Repeat("x", 1<<20+1)},
		{"missing terminator", `{<<"name">>,<<"decimal">>}`},
		{"not a tuple", `<<"name">>.`},
		{"wrong tuple size", `{<<"name">>}.`},
		{"nonstring key", `{12,<<"x">>}.`},
		{"duplicate key", `{<<"name">>,<<"one">>}.{<<"name">>,<<"two">>}.`},
		{"unknown atom", `{<<"name">>,undefined}.`},
		{"bad integer", `{<<"number">>,-}.`},
		{"bad escape", `{<<"name">>,<<"bad\q">>}.`},
		{"unterminated binary", `{<<"name">>,<<"bad}.`},
		{"invalid utf8", `{<<"name">>,<<"` + string([]byte{0xff}) + `">>}.`},
		{"missing sequence comma", `{<<"list">>,[1 2]}.`},
		{"too deep", `{<<"list">>,` + deep + `}.`},
		{"trailing garbage", `{<<"name">>,<<"x">>}. garbage`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseMetadata([]byte(tc.source)); err == nil {
				t.Fatalf("accepted malformed Erlang terms: %.80q", tc.source)
			}
		})
	}
	if _, err := parseMetadata([]byte(`{<<"list">>,[` +
		strings.Repeat("1,", 20001) + `1]}.`)); err == nil {
		t.Fatal("accepted unbounded terms")
	}
}
