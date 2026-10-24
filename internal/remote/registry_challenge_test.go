package remote

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestUnitRegistryChallengeParsing(t *testing.T) {
	for _, header := range []string{
		`Bearer realm="https://auth.example/token",service="registry.example",scope="repository:team/image:pull"`,
		`basic realm="local", BeArEr realm="https://auth.example/token", service=registry.example, scope="repository:team/image:pull,push"`,
		`Digest realm="other", Bearer realm="https://auth.example/token",service="registry.example"`,
	} {
		got, err := parseRegistryChallenge([]string{header}, "team/image", true)
		if err != nil || got.realm != "https://auth.example/token" ||
			got.service != "registry.example" ||
			got.scope != "repository:team/image:pull" {
			t.Fatal(got, err)
		}
	}
	for _, header := range []string{
		`Bearer`, `Bearer realm="https://auth.example/token`, `Bearer realm="https://auth.example/token",realm="https://evil.example/token"`,
		`Bearer realm="https://auth.example/token", scope="repository:other/image:pull"`,
		`Bearer realm="https://auth.example/token", scope="repository:team/image:push"`,
		`Bearer realm="https://auth.example/token", scope="repository:team/image:pull repository:other/image:pull"`,
		`Bearer realm="https://auth.example/token", scope="registry:catalog:*"`,
		"Bearer realm=\"https://auth.example/token\", service=\"bad\nservice\"",
		`Bearer realm=`, `Bearer =missing`, `realm="https://auth.example/token"`, `Bearer realm="https://auth.example/token",`,
		`Bearer realm="https://user:password@auth.example/token"`, `Bearer realm="file:///etc/passwd"`, "Bearer realm=\x00",
		`Be(arer realm="https://auth.example/token"`, `Bearer realm=has spaces`,
		strings.Repeat("x", 16385), `Bearer realm="bad"quote"`,
	} {
		if _, err := parseRegistryChallenge([]string{header}, "team/image", true); err == nil {
			t.Errorf("accepted %q", header)
		}
	}
	if _, err := parseRegistryChallenge(nil, "image", true); !errors.Is(err, ErrRegistryChallenge) {
		t.Fatal(err)
	}
	if _, err := parseRegistryChallenge(
		[]string{`Basic realm="one"`, `Bearer realm="https://auth.example/token"`},
		"image",
		false,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := registryScope("repository:image:pull", " "); err == nil {
		t.Fatal("scope accepted")
	}
	c, err := parseRegistryChallenge(
		[]string{`Bearer realm="https://auth.example/token?x=a,b",service="escaped\"service"`},
		"image",
		true,
	)
	if err != nil || c.service != `escaped"service` {
		t.Fatal(c, err)
	}
}

func TestUnitRegistryTokenURLAndResponseValidation(t *testing.T) {
	c := registryChallenge{
		realm:   "https://auth.example/token?opaque=keep&scope=repository:evil:push&service=wrong&account=wrong&offline_token=true",
		service: "expected",
		scope:   "repository:team/image:pull",
	}
	got, err := registryTokenURL(c)
	if err != nil ||
		got != "https://auth.example/token?client_id=artifactweave&opaque=keep&scope=repository%3Ateam%2Fimage%3Apull&service=expected" {
		t.Fatal(got, err)
	}
	c.realm = "https://auth.example/token?bad=%zz"
	if _, err := registryTokenURL(c); err == nil {
		t.Fatal("bad query")
	}
	c.realm = "https://bad%host/token"
	if _, err := registryTokenURL(c); err == nil {
		t.Fatal("bad realm")
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, body := range []string{
		`{"token":"opaque"}`, `{"access_token":"opaque"}`, `{"token":"opaque","access_token":"opaque"}`,
		`{"token":"opaque","expires_in":9223372036854775807}`, `{"token":"opaque","scope":"repository:team/image:pull","expires_in":1}`,
		fmt.Sprintf(`{"token":"opaque","issued_at":%q}`, now.Format(time.RFC3339)),
	} {
		token, err := decodeRegistryToken([]byte(body), "repository:team/image:pull", now)
		if err != nil || token.value != "opaque" || !token.expires.After(now) {
			t.Fatal(body, token, err)
		}
	}
	for _, body := range []string{
		`null`, `{}`, `[]`, `not json`, `{"token":""}`, `{"token":"has spaces"}`, `{"token":"bad\r\nheader"}`,
		`{"token":"one","access_token":"two"}`, `{"token":"opaque","scope":""}`, `{"token":"opaque","expires_in":0}`,
		`{"token":"opaque","expires_in":-1}`, `{"token":"opaque","expires_in":1.2}`, `{"token":"opaque","issued_at":"not-time"}`,
		fmt.Sprintf(`{"token":"opaque","issued_at":%q}`, now.Add(time.Hour).Format(time.RFC3339)),
		fmt.Sprintf(`{"token":"opaque","issued_at":%q}`, now.Add(-time.Hour).Format(time.RFC3339)),
		`{"token":"opaque"} {}`, `{"token":"opaque"} trailing`,
		`{"token":"` + strings.Repeat("a", 65537) + `"}`,
	} {
		if _, err := decodeRegistryToken(
			[]byte(body),
			"repository:team/image:pull",
			now,
		); err == nil {
			t.Errorf("accepted token body %.120s", body)
		}
	}
}

func TestUnitRegistryQuotedPairGrammar(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"escaped\nvalue"`, "escapednvalue"}, {`"escaped\:value"`, "escaped:value"}, {`"escaped\\value"`, `escaped\value`},
	} {
		got, err := registryQuotedValue(tc.raw)
		if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
	}
	for _, raw := range []string{`"`, `"unfinished`, `"ends\"`, `"inner"quote"`} {
		if _, err := registryQuotedValue(raw); err == nil {
			t.Fatal("malformed quoted value accepted", raw)
		}
	}
}
