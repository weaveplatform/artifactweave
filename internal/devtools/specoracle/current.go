package specoracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const groupsFamily = "administration-jfrog-platform-groups-api"

var (
	errCurrentSource = errors.New("specoracle: current family source is not verified")
	readmeProps      = regexp.MustCompile(`(?s)<script\b[^>]*\bid="ssr-props"[^>]*>(.*?)</script>`)
	readmeHandle     = regexp.MustCompile(`^@administration/v([a-zA-Z0-9._-]+)#([a-zA-Z0-9_-]+)$`)
)

type currentFamilySource struct{ page, schema, title string }

// These are evidenced current publications, not guessed replacements for any
// failed index. Resolve the live handle each time; registry IDs may change.
var currentFamilySources = map[string]currentFamilySource{
	groupsFamily: {"groups", "groups-openapi.yml", "JFrog Platform - Groups API"},
	"administration-jfrog-platform-artifactory-system-api": {"getclusterlocks", "artifactory-system-api.yaml", "JFrog Platform - Artifactory System API"},
	"administration-jfrog-platform-aws-iam-api":            {"getawsiamrole", "aws-iam-openapi.yml", "JFrog Platform - AWS IAM API"},
	"administration-jfrog-platform-access-federation-api":  {"getallaccessfederations", "federation-openapi.yml", "JFrog Platform - Access Federation API"},
}

type readmePage struct {
	Slug   string       `json:"slug"`
	Type   string       `json:"type"`
	Method string       `json:"api_method"`
	Pages  []readmePage `json:"pages"`
}

type readmeSource struct {
	Handle   string `json:"oasPublicUrl"` //nolint:tagliatelle // ReadMe's published wire name.
	Document struct {
		Parent struct {
			URI string `json:"uri"`
		} `json:"parent"`
		API struct {
			URI    string         `json:"uri"`
			Method string         `json:"method"`
			Path   string         `json:"path"`
			Schema map[string]any `json:"schema"`
		} `json:"api"`
	} `json:"document"`
	Sidebar []readmePage `json:"sidebar"`
}

// fetchCurrentFamily follows an evidenced current page to its endorsed full
// definition. Historical URLs are anchors, not perpetual family membership.
func (f *fetcher) fetchCurrentFamily(ctx context.Context, rec FamilyRecord, source currentFamilySource) (FamilyRecord, []ManifestEntry) {
	u, err := url.Parse(rec.Index)
	if err != nil {
		rec.Error += "; " + err.Error()
		return rec, nil
	}
	u.Path = path.Join(path.Dir(path.Dir(u.Path)), source.page)
	pageURL := u.String()
	body, status, err := f.getAccept(ctx, pageURL, "text/html")
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("%s: status %d: %w", pageURL, status, ErrBadStatus)
	}
	var props readmeSource
	if err == nil {
		match := readmeProps.FindSubmatch(body)
		if match == nil {
			err = errCurrentSource
		} else {
			err = json.Unmarshal(match[1], &props)
		}
	}
	var registry string
	var declared []readmePage
	if err == nil {
		registry, declared, err = props.definition(source)
	}
	if err != nil {
		rec.Error += "; current hierarchy: " + err.Error()
		return rec, nil
	}
	body, status, err = f.getAccept(ctx, registry, "application/json")
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("%s: status %d: %w", registry, status, ErrBadStatus)
	}
	if err == nil {
		err = verifyCurrentFamily(body, source, props, declared)
	}
	if err != nil {
		rec.Error += "; current definition: " + err.Error()
		return rec, nil
	}
	rec.Source, rec.Section, rec.Discovery = registry, source.title, pageURL
	entry := ManifestEntry{Index: rec.Index, Family: rec.Family, Title: rec.Section, URL: registry, Status: status, Canonical: true}
	entry.File = rec.Family + "/definition.json"
	sum := sha256.Sum256(body)
	entry.SHA256 = hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(f.cacheDir, rec.Family), 0o750); err != nil {
		entry.Error = fmt.Sprintf("create current family directory: %v", err)
	} else if err := os.WriteFile(filepath.Join(f.cacheDir, filepath.FromSlash(entry.File)), body, 0o600); err != nil {
		entry.Error = fmt.Sprintf("cache current family definition: %v", err)
	}
	if entry.Error != "" {
		entry.File = ""
		rec.Source = ""
		rec.Error += "; " + entry.Error
	}
	return rec, []ManifestEntry{entry}
}

func (s readmeSource) definition(source currentFamilySource) (string, []readmePage, error) {
	m := readmeHandle.FindStringSubmatch(s.Handle)
	if m == nil {
		return "", nil, errCurrentSource
	}
	branch := "/branches/" + m[1]
	if s.Document.API.URI != branch+"/apis/"+source.schema {
		return "", nil, errCurrentSource
	}
	var declared []readmePage
	if source.page == "groups" {
		if s.Document.Parent.URI != branch+"/reference/groups" {
			return "", nil, errCurrentSource
		}
		var groups []readmePage
		var visit func([]readmePage)
		visit = func(pages []readmePage) {
			for _, p := range pages {
				if p.Slug == "groups" {
					groups = append(groups, p)
				} else {
					visit(p.Pages)
				}
			}
		}
		visit(s.Sidebar)
		if len(groups) != 1 || len(groups[0].Pages) == 0 {
			return "", nil, errCurrentSource
		}
		declared = groups[0].Pages
	}
	return "https://dash.readme.com/api/v1/api-registry/" + m[2], declared, nil
}

func verifyCurrentFamily(body []byte, source currentFamilySource, endorsement readmeSource, pages []readmePage) error {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("decode full definition: %w", err)
	}
	info, _ := doc["info"].(map[string]any)
	version, _ := doc["openapi"].(string)
	if info["title"] != source.title || !strings.HasPrefix(version, "3.") {
		return errCurrentSource
	}
	o := Oracle{Ops: map[string]Operation{}}
	o.add(doc, "", "")
	if len(o.Ops) == 0 {
		return errCurrentSource
	}
	api := endorsement.Document.API
	pageInfo, _ := api.Schema["info"].(map[string]any)
	if pageInfo["title"] != source.title || !isMethod(strings.ToLower(api.Method)) {
		return errCurrentSource
	}
	page := Oracle{Ops: map[string]Operation{}}
	page.add(api.Schema, "", "")
	key := strings.ToUpper(api.Method) + " " + NormalisePath(joinPath(serverBasePath(api.Schema), api.Path))
	endorsing, pageOK := page.Ops[key]
	published, fullOK := o.Ops[key]
	if !pageOK || !fullOK || endorsing.ID == "" || endorsing.ID != published.ID {
		return errCurrentSource
	}
	return verifyAdvertisedOperations(o, pages)
}

func verifyAdvertisedOperations(o Oracle, pages []readmePage) error {
	seen := map[string]bool{}
	for _, page := range pages {
		if page.Type != "endpoint" || !isMethod(page.Method) || seen[page.Slug] {
			return errCurrentSource
		}
		seen[page.Slug] = true
		found := false
		for _, op := range o.Ops {
			if op.Method == strings.ToUpper(page.Method) && strings.EqualFold(op.ID, page.Slug) {
				found = true
			}
		}
		if !found {
			return errCurrentSource
		}
	}
	return nil
}
