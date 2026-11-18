package npm

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

// External is one supported dependency URL and its synthetic dist-tag identity.
type External struct{ Name, Spec, File, URL string }

// ExternalDependency recognizes tarballs and GitHub's archive-capable git forms.
// Unknown hosts retain their original git resolver; there is no generic archive
// endpoint or safe way to infer one from an arbitrary SSH locator.
func ExternalDependency(name, spec string, versioning bool) (External, bool) {
	if !ValidName(name) || len(spec) > 4096 {
		return External{}, false
	}
	target, file, ok := githubDependency(spec)
	if !ok {
		u, err := url.Parse(spec)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
			return External{}, false
		}
		base := path.Base(u.Path)
		if base == "." || base == "/" || base == "" {
			return External{}, false
		}
		file = strings.ReplaceAll(strings.TrimPrefix(name, "@"), "/", "-") + "-" + base
		if versioning {
			file = externalVersionFile(file, u.Path)
		}
		target = u.String()
	}
	if len(file) > 240 || strings.ContainsAny(file, "/%\\\x00\r\n?#:") || file == "." || file == ".." {
		return External{}, false
	}
	return External{Name: name, File: file, Spec: externalTag(file) + "-art-external", URL: target}, true
}

// npm dist-tags must survive encodeURIComponent unchanged. Escape every other
// byte, including the escape delimiter, so normalization cannot alias two names.
func externalTag(file string) string {
	var out strings.Builder
	for i := 0; i < len(file); i++ {
		b := file[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '.' {
			out.WriteByte(b)
		} else {
			out.WriteByte('_')
			out.WriteString(hex.EncodeToString([]byte{b}))
			out.WriteByte('_')
		}
	}
	return out.String()
}

var externalURLVersion = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+)+(?:[-+][A-Za-z0-9.-]+)?$`)

func externalVersionFile(file, p string) string {
	parts := strings.Split(path.Dir(p), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		version := parts[i]
		if !externalURLVersion.MatchString(version) {
			continue
		}
		if strings.Contains(file, version) {
			return file
		}
		suffix := path.Ext(file)
		if strings.HasSuffix(file, ".tar.gz") {
			suffix = ".tar.gz"
		}
		return strings.TrimSuffix(file, suffix) + "-" + version + suffix
	}
	return file
}

func githubDependency(spec string) (string, string, bool) {
	raw := spec
	for _, prefix := range []string{"git+https://", "git+http://", "git+ssh://", "git://", "ssh://"} {
		if strings.HasPrefix(raw, prefix) {
			raw = strings.TrimPrefix(raw, prefix)
			raw = strings.TrimPrefix(raw, "git@")
			raw = strings.TrimPrefix(raw, "github.com:")
			if strings.HasPrefix(raw, "github.com/") {
				raw = strings.TrimPrefix(raw, "github.com/")
			} else if !strings.HasPrefix(spec, prefix+"git@github.com:") && !strings.HasPrefix(spec, prefix+"github.com:") {
				return "", "", false
			}
			break
		}
	}
	raw = strings.TrimPrefix(raw, "github:")
	// HTTPS hosted repositories use the same two-segment shape as shorthand.
	raw = strings.TrimPrefix(raw, "https://github.com/")
	project, ref, hasRef := strings.Cut(raw, "#")
	parts := strings.Split(strings.TrimSuffix(project, ".git"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	for _, part := range parts {
		if part == "." || part == ".." || strings.ContainsAny(part, "@: %\\\r\n?") {
			return "", "", false
		}
	}
	if !hasRef {
		ref = "HEAD"
	}
	if ref == "" || strings.ContainsAny(ref, "%\\\x00\r\n?#:") {
		return "", "", false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || part == "." || part == ".." {
			return "", "", false
		}
	}
	file := parts[0] + "-" + parts[1] + "-" + strings.ReplaceAll(ref, "/", "-") + ".tar.gz"
	return "https://github.com/" + parts[0] + "/" + parts[1] + "/archive/" + ref + ".tar.gz", file, true
}

// RewriteDependencies changes only string specs in the three supported fields.
// Every render owns its decoded JSON, preserving cached bytes and dist.signatures.
func RewriteDependencies(manifest json.RawMessage, rewrite func(name, spec string) (string, error)) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(manifest, &fields); err != nil {
		return nil, fmt.Errorf("npm dependency manifest: %w", err)
	}
	for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
		var deps map[string]json.RawMessage
		if json.Unmarshal(fields[key], &deps) != nil || deps == nil {
			continue
		}
		names := make([]string, 0, len(deps))
		for name := range deps {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			var spec string
			if json.Unmarshal(deps[name], &spec) != nil {
				continue
			}
			replacement, err := rewrite(name, spec)
			if err != nil {
				return nil, err
			}
			if replacement != spec {
				deps[name], _ = json.Marshal(replacement)
			}
		}
		fields[key], _ = json.Marshal(deps)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("npm dependency document: %w", err)
	}
	return out, nil
}
