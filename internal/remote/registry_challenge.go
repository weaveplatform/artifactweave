package remote

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/http/httpguts"
)

type registryChallenge struct{ scheme, realm, service, scope string }

func (c registryChallenge) key() string { return c.realm + "\x00" + c.service + "\x00" + c.scope }

// Split only unquoted commas: realm queries and scope action lists can contain
// commas. Duplicate parameters are rejected instead of depending on header order.
func challengeFields(values []string) ([]string, error) {
	var fields []string
	total := 0
	for _, value := range values {
		total += len(value)
		if total > 16384 {
			return nil, ErrRegistryChallenge
		}
		start, quoted, escaped := 0, false, false
		for i, c := range value {
			if c < 32 && c != '\t' || c == 127 {
				return nil, ErrRegistryChallenge
			}
			if escaped {
				escaped = false
				continue
			}
			if quoted && c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				quoted = !quoted
			}
			if c == ',' && !quoted {
				fields = append(fields, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
		if quoted || escaped {
			return nil, ErrRegistryChallenge
		}
		fields = append(fields, strings.TrimSpace(value[start:]))
	}
	return fields, nil
}

type challengeParameters struct {
	scheme string
	values map[string]string
}

func registryChallenges(values []string) ([]challengeParameters, error) {
	fields, err := challengeFields(values)
	if err != nil {
		return nil, err
	}
	var challenges []challengeParameters
	for _, field := range fields {
		if field == "" {
			return nil, ErrRegistryChallenge
		}
		first := strings.IndexFunc(field, unicode.IsSpace)
		equals := strings.IndexByte(field, '=')
		if first >= 0 && (equals < 0 || first < equals) &&
			!strings.HasPrefix(strings.TrimSpace(field[first:]), "=") {
			scheme := strings.ToLower(field[:first])
			if !httpguts.ValidHeaderFieldName(scheme) {
				return nil, ErrRegistryChallenge
			}
			challenges = append(
				challenges,
				challengeParameters{scheme: scheme, values: make(map[string]string)},
			)
			field = strings.TrimSpace(field[first:])
		} else if equals < 0 {
			challenges = append(
				challenges,
				challengeParameters{
					scheme: strings.ToLower(field),
					values: make(map[string]string),
				},
			)
			continue
		}
		if len(challenges) == 0 {
			return nil, ErrRegistryChallenge
		}
		key, value, ok := strings.Cut(field, "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		if !ok || !httpguts.ValidHeaderFieldName(key) {
			return nil, ErrRegistryChallenge
		}
		if strings.HasPrefix(value, "\"") {
			value, err = registryQuotedValue(value)
			if err != nil {
				return nil, ErrRegistryChallenge
			}
		} else if !httpguts.ValidHeaderFieldName(value) {
			return nil, ErrRegistryChallenge
		}
		params := challenges[len(challenges)-1].values
		if _, duplicate := params[key]; duplicate {
			return nil, ErrRegistryChallenge
		}
		params[key] = value
	}
	return challenges, nil
}

func parseRegistryChallenge(values []string, image string, tokens bool) (registryChallenge, error) {
	challenges, err := registryChallenges(values)
	if err != nil {
		return registryChallenge{}, err
	}
	basic := false
	for _, parsed := range challenges {
		if parsed.scheme == "basic" {
			basic = true
			continue
		}
		if parsed.scheme != "bearer" || !tokens {
			continue
		}
		realm, err := checkedURL(parsed.values["realm"])
		if err != nil {
			return registryChallenge{}, ErrRegistryChallenge
		}
		service := parsed.values["service"]
		if len(service) > 1024 || strings.ContainsAny(service, "\x00\r\n") {
			return registryChallenge{}, ErrRegistryChallenge
		}
		scope, err := registryScope(parsed.values["scope"], image)
		if err != nil {
			return registryChallenge{}, err
		}
		return registryChallenge{
			scheme:  "bearer",
			realm:   realm.String(),
			service: service,
			scope:   scope,
		}, nil
	}
	if basic {
		return registryChallenge{scheme: "basic"}, nil
	}
	return registryChallenge{}, ErrRegistryChallenge
}

func registryScope(challenged, image string) (string, error) {
	expected := ""
	if image != "" {
		expected = "repository:" + image + ":pull"
	}
	if challenged == "" {
		return expected, nil
	}
	// A read-only adapter never asks for push, catalog or another image's access.
	// Registries may advertise pull,push; narrow that single resource to pull.
	prefix, actions, found := strings.Cut(challenged, ":")
	name, actions, second := strings.Cut(actions, ":")
	if !found || !second || prefix != "repository" || image == "" || name != image {
		return "", ErrRegistryDenied
	}
	for _, action := range strings.Split(actions, ",") {
		if action == "pull" {
			return expected, nil
		}
	}
	return "", ErrRegistryDenied
}

func registryTokenURL(c registryChallenge) (string, error) {
	u, err := url.Parse(c.realm)
	if err != nil {
		return "", ErrRegistryChallenge
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ErrRegistryChallenge
	}
	for _, key := range []string{"service", "scope", "account", "offline_token", "client_id"} {
		query.Del(key)
	}
	if c.service != "" {
		query.Set("service", c.service)
	}
	if c.scope != "" {
		query.Set("scope", c.scope)
	}
	query.Set("client_id", "artifactweave")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// HTTP quoted-pair escapes the following visible character literally. Go string
// escapes would wrongly interpret backslash-n and reject valid escaped punctuation.
func registryQuotedValue(value string) (string, error) {
	if len(value) < 2 || value[len(value)-1] != '"' {
		return "", ErrRegistryChallenge
	}
	var out strings.Builder
	for i := 1; i < len(value)-1; i++ {
		char := value[i]
		switch char {
		case '\\':
			i++
			if i >= len(value)-1 {
				return "", ErrRegistryChallenge
			}
			char = value[i]
		case '"':
			return "", ErrRegistryChallenge
		}
		out.WriteByte(char)
	}
	return out.String(), nil
}
