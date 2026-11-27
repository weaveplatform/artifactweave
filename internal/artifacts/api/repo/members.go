package repo

import (
	"fmt"
	"net/url"
	"strings"
)

// MemberMode is how a federated repository member takes part in the
// federation (decision 0022). An empty mode is bidirectional: 7.x's
// default when the field is absent.
type MemberMode string

// Member modes as 7.x's repositories API spells them. The XSD's description
// says "unidirectional", but the REST example and the documentation send
// "receiver" (research 40, section 2), and any other value is refused.
const (
	ModeBidirectional MemberMode = "bidirectional"
	ModeReceiver      MemberMode = "receiver"
)

// MaxMembers is how many members a federation may have, this instance's
// own included, as 7.x's Federation Members and Topology page states.
const MaxMembers = 10

// SelfMemberBase stands for this instance's base URL in a stored member
// URL, as 7.161.26 stores its own member as BASE_URL_PLACEHOLDER/<repoKey>
// (FederatedMemberUtils.java:95-99). Reads resolve it against
// federatedRepoUrlBase, or urlBase when that is empty, so changing either
// changes the self member every read returns without rewriting a stored
// repository. It is not a URL, so it cannot match a member a client sends.
const SelfMemberBase = "{self}"

// SelfMemberURL is the stored URL of a repository's own member.
func SelfMemberURL(key string) string { return SelfMemberBase + "/" + key }

// NormalizeMemberBase applies 7.x's base URL rule (normalizeBaseUrl,
// FederatedMemberUtils.java:76-79): trailing slashes are trimmed and
// /artifactory is appended when the base does not end in it.
func NormalizeMemberBase(base string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/artifactory") {
		base += "/artifactory"
	}
	return base
}

// SplitMemberURL splits a member URL into its normalized base URL and its
// repository key, the URL's last path element (FederatedMemberUtils.java:127
// and :145). The stored self member gives SelfMemberBase as its base. A URL
// that is not http or https with a host, or that has a query, fragment, user
// information or no key, is refused.
func SplitMemberURL(raw string) (base, key string, err error) {
	if rest, ok := strings.CutPrefix(raw, SelfMemberBase+"/"); ok && rest != "" && !strings.Contains(rest, "/") {
		return SelfMemberBase, rest, nil
	}
	u, perr := url.Parse(raw)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", "", fmt.Errorf("%w: member URL %q is not an http or https URL", ErrInvalid, raw)
	}
	path := strings.TrimRight(u.Path, "/")
	i := strings.LastIndex(path, "/")
	if i < 0 || path[i+1:] == "" {
		return "", "", fmt.Errorf("%w: member URL %q does not end in a repository key", ErrInvalid, raw)
	}
	key = path[i+1:]
	u.Path, u.RawPath = path[:i], ""
	return NormalizeMemberBase(u.String()), key, nil
}

// MemberURL joins a normalized base URL and a repository key.
func MemberURL(base, key string) string { return base + "/" + key }

// ResolveSelfMember rewrites the stored self member of c to selfBase, the
// instance's federatedRepoUrlBase or urlBase, as every read returns it. A
// blank selfBase leaves the stored form, which names no host. Members are
// copied, so a configuration shared with a store is not changed.
func ResolveSelfMember(c *Config, selfBase string) {
	if len(c.Members) == 0 || strings.TrimSpace(selfBase) == "" {
		return
	}
	members := make([]Member, len(c.Members))
	copy(members, c.Members)
	for i, m := range members {
		if base, key, err := SplitMemberURL(m.URL); err == nil && base == SelfMemberBase {
			members[i].URL = MemberURL(NormalizeMemberBase(selfBase), key)
		}
	}
	c.Members = members
}

// validateMembers applies the member rules that need no other repository or
// peer: at most MaxMembers, a URL whose last path element is a key, and a
// known mode. Whether each member is this instance or a paired peer is the
// federation module's check, which needs its peers and base URL.
func validateMembers(c *Config, add func(field, msg string)) {
	if len(c.Members) == 0 {
		return
	}
	if len(c.Members) > MaxMembers {
		add("members", fmt.Sprintf("allows at most %d members, this instance's included", MaxMembers))
	}
	for _, m := range c.Members {
		if _, _, err := SplitMemberURL(m.URL); err != nil {
			add("members.url", fmt.Sprintf("%q must be an http or https URL that ends in the member's repository key", m.URL))
		}
		switch m.Mode {
		case "", ModeBidirectional, ModeReceiver:
		default:
			add("members.mode", fmt.Sprintf("%q is not one of %s, %s", m.Mode, ModeBidirectional, ModeReceiver))
		}
	}
}
