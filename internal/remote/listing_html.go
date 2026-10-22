package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func parseHTMLListing(
	ctx context.Context,
	body io.Reader,
	rawBase, folder string,
) (listingPage, error) {
	var page listingPage
	base, err := listingBase(rawBase)
	if err != nil {
		return page, err
	}
	limited := &io.LimitedReader{R: body, N: listingBytes + 1}
	tokens := html.NewTokenizer(limited)
	tokens.SetMaxBuf(listingBytes)
	index, heading := false, false
	for {
		if err = ctx.Err(); err != nil {
			return page, fmt.Errorf("parse listing: %w", err)
		}
		switch tokens.Next() {
		case html.ErrorToken:
			if limited.N <= 0 || !errors.Is(tokens.Err(), io.EOF) {
				return page, listingError()
			}
			if !index && len(page.entries) == 0 {
				return page, source.ErrUnsupported
			}
			return page, nil
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokens.Token()
			heading = token.Data == "title" || token.Data == "h1"
			if token.Data != "a" && token.Data != "link" {
				continue
			}
			if err = listingLink(&page, token, base, folder); err != nil {
				return page, err
			}
		case html.EndTagToken:
			heading = false
		case html.TextToken:
			if heading &&
				strings.HasPrefix(
					strings.ToLower(strings.TrimSpace(string(tokens.Text()))),
					"index of",
				) {
				index = true
			}
		}
	}
}

func listingLink(page *listingPage, token html.Token, base *url.URL, folder string) error {
	var href, rel string
	for _, attr := range token.Attr {
		switch attr.Key {
		case "href":
			href = attr.Val
		case "rel":
			rel = attr.Val
		}
	}
	if href == "" {
		return nil
	}
	page.count++
	if page.count > 10000 {
		return listingError()
	}
	next := false
	for _, relation := range strings.Fields(rel) {
		if strings.EqualFold(relation, "next") {
			next = true
		}
	}
	u, ok := listingLinkURL(base, href)
	if !ok {
		if next {
			return listingError()
		}
		return nil
	}
	if next {
		if u.Path != base.Path || u.RawQuery == base.RawQuery ||
			(page.next != "" && page.next != u.String()) {
			return listingError()
		}
		page.next = u.String()
		return nil
	}
	if token.Data != "a" {
		return nil
	}
	prefix := strings.TrimRight(base.Path, "/") + "/"
	if !strings.HasPrefix(u.Path, prefix) {
		return nil
	}
	child := strings.TrimPrefix(u.Path, prefix)
	isFolder := strings.HasSuffix(child, "/")
	child = strings.TrimSuffix(child, "/")
	if child == "" || strings.Contains(child, "/") {
		return nil
	}
	if _, _, _, err := types.SplitItemPath(child); err != nil {
		return nil
	}
	childPath := child
	if folder != "" {
		childPath = folder + "/" + child
	}
	page.entries = append(page.entries, ListingEntry{Path: childPath, Folder: isFolder})
	return nil
}

func listingLinkURL(base *url.URL, raw string) (*url.URL, bool) {
	if len(raw) > 8192 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" ||
		strings.ContainsAny(u.Path, "\\;") || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 {
		return nil, false
	}
	// ResolveReference removes dot segments. Reject them before resolution so a
	// parent/traversal link cannot become an apparently harmless direct child.
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == ".." {
			return nil, false
		}
	}
	u = base.ResolveReference(u)
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return nil, false
	}
	return u, true
}
