package httpkit

import "strings"

// ChiPatterns converts an OpenAPI path template to chi patterns. A trailing
// parameter is registered twice, once as a single segment and once as a
// wildcard, because Artifactory item paths ({itemPath}, {repoPath}) span
// segments and OpenAPI cannot say so.
func ChiPatterns(template string) []string {
	// Artifactory reserves "api" as a repository key, so an item route such as
	// /artifactory/{repoKey}/{filePath} must never match /artifactory/api/...;
	// otherwise chi backtracks into it for every unknown API path and answers
	// 501 where the client should see 404 or 405. Go's regexp has no
	// lookahead, so "anything but api" is spelled out.
	template = strings.Replace(
		template,
		"/artifactory/{repoKey}/",
		"/artifactory/{repoKey:"+NotAPI+"}/",
		1,
	)
	patterns := []string{template}
	segments := strings.Split(template, "/")
	last := segments[len(segments)-1]
	if strings.HasPrefix(last, "{") && strings.HasSuffix(last, "}") {
		segments[len(segments)-1] = "*"
		patterns = append(patterns, strings.Join(segments, "/"))
	}
	return patterns
}

// NotAPI is a chi pattern regular expression that matches one path segment
// other than the literal "api", the key Artifactory reserves.
const NotAPI = `(?:[^a/][^/]*|a(?:[^p/][^/]*)?|ap(?:[^i/][^/]*)?|api[^/]+)`
