package docker_test

import (
	"strings"
	"testing"
)

type statusCase struct {
	route, path, as, body string
	want                  int
}

func (c statusCase) method() string {
	method, _, _ := strings.Cut(c.route, " ")
	return method
}

func dockerStatusRows() []statusCase {
	rows := registryStatusRows()
	rows = append(rows, registryBlobStatusRows()...)
	rows = append(rows, registryManifestStatusRows()...)
	rows = append(rows, registryReferrersStatusRows()...)
	rows = append(rows, registryListingStatusRows()...)
	return rows
}

func TestDockerStatusTable(t *testing.T) {
	off, secrets := newStatusFixture(t, false)
	on := statusFixture(t, true)
	var replacements []string
	for name, value := range secrets {
		replacements = append(replacements, "{"+name+"}", value)
	}
	fill := strings.NewReplacer(replacements...)
	for _, c := range dockerStatusRows() {
		h := off
		var headers map[string]string
		switch c.as {
		case "anonymous":
			h = on
		case "admin", "user":
			headers = basic(c.as, "pw")
		case "expired":
			headers = bearerAuth(secrets["expired-access"])
		case "forged":
			headers = bearerAuth(secrets["forged-access"])
		default:
			if user, password, ok := strings.Cut(c.as, ":"); ok {
				headers = basic(user, password)
			} else if token, ok := secrets[c.as+"-access"]; ok {
				headers = bearerAuth(token)
			}
		}
		body := fill.Replace(c.body)
		if body != "" {
			if headers == nil {
				headers = map[string]string{}
			}
			headers["Content-Type"], body = contentTypeOf(body)
		}
		rec := do(t, h, c.method(), fill.Replace(c.path), headers, body)
		if rec.Code != c.want {
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}
