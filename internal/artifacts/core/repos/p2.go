package repos

import (
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/p2"
)

// PlanP2Virtual gives a P2 virtual repository c the members its p2Urls
// need, as 7.x's UI does when it saves one (6.20's
// CreateRepoConfigHelper.validateAndCreateP2ConfigRepos), for a create or
// update through the repositories API. known holds every stored repository.
// It returns c with the members it adds appended in p2Urls order, and the
// P2 remote repositories to create before c, in the same transaction.
//
// An http or https URL that a member remote already covers (p2.Covers)
// needs nothing; one that a stored P2 remote outside c covers adds that
// remote, as the UI matches every remote's url; any other creates a P2
// remote whose url is the URL's root, <scheme>://<host>[:<port>]/, or for
// a site archive's entry (site.zip!/) the URL itself, which only it covers
// [I]. Its key is the host, or host-port when the URL names a port
// (RTFACT-30256), and the UI's next free <key>-1, <key>-2 when another
// repository holds that key. A local://<key> URL adds that local Generic or
// Maven repository. A created remote has the defaults and checks of a
// direct create (PrepareCreate), so a key the rules refuse answers that
// create's 400, and lists its remote folders, which the UI sets for the
// P2 remotes it creates. Unlike the UI, which replaces the members with
// those the URLs name, nothing is taken out of repositories [I]: a request
// to the API names its members itself.
func PlanP2Virtual(c repo.Config, known []repo.Config) (repo.Config, []repo.Config, error) {
	if c.Class != repo.Virtual || c.PackageType != repo.P2 || len(c.P2URLs) == 0 {
		return c, nil, nil
	}
	c.Repositories = slices.Clone(c.Repositories)
	// c is in all because a create's known does not hold it yet, and a
	// remote must not take the key of the virtual repository being created.
	all := append(slices.Clone(known), c)
	var created []repo.Config
	for _, raw := range c.P2URLs {
		site, err := p2.ParseSiteURL(raw)
		if err != nil {
			return c, nil, badRequestf(
				"Invalid P2 URL '%s': expected local://<key>[/<suffix>] or an http or https URL",
				raw,
			)
		}
		if site.Local() {
			i := slices.IndexFunc(all, func(k repo.Config) bool { return k.Key == site.Key })
			if i < 0 || all[i].Class != repo.Local ||
				!repo.AcceptsMember(repo.P2, all[i].PackageType) {
				return c, nil, badRequestf(
					"P2 URL '%s' names no local Generic or Maven repository", raw)
			}
			if !slices.Contains(c.Repositories, site.Key) {
				c.Repositories = append(c.Repositories, site.Key)
			}
			continue
		}
		if _, _, ok := p2.SiteChild(site, p2MembersOf(c.Repositories, all)); ok {
			continue
		}
		if key, _, ok := p2.SiteChild(site, p2MembersOf(remoteKeys(all), all)); ok {
			c.Repositories = append(c.Repositories, key)
			continue
		}
		remote, err := PrepareCreate(p2Remote(site, all), all)
		if err != nil {
			return c, nil, err
		}
		created = append(created, remote)
		all = append(all, remote)
		c.Repositories = append(c.Repositories, remote.Key)
	}
	return c, created, nil
}

// p2Remote is the P2 remote repository PlanP2Virtual creates for site, an
// http or https URL no repository covers, under the first key no
// repository in all holds.
func p2Remote(site p2.SiteURL, all []repo.Config) repo.Config {
	base := strings.ToLower(site.URL.Hostname())
	if port := site.URL.Port(); port != "" {
		base += "-" + port
	}
	key := base
	for n := 1; slices.ContainsFunc(all, func(k repo.Config) bool {
		return strings.EqualFold(k.Key, key)
	}); n++ {
		key = fmt.Sprintf("%s-%d", base, n)
	}
	url := site.URL.Scheme + "://" + strings.ToLower(site.URL.Host) + "/"
	if strings.Contains(site.Raw, "!/") {
		url = site.Raw
	}
	return repo.Config{
		Key: key, Class: repo.Remote, PackageType: repo.P2, URL: url,
		ListRemoteFolderItems: repo.Flex(true),
	}
}

// p2MembersOf returns the repositories in all that keys name, in keys'
// order, as the p2Urls rules see them: a P2 remote with its url.
func p2MembersOf(keys []string, all []repo.Config) []p2.Member {
	out := make([]p2.Member, 0, len(keys))
	for _, key := range keys {
		i := slices.IndexFunc(all, func(k repo.Config) bool { return k.Key == key })
		if i < 0 {
			continue
		}
		m := p2.Member{Key: key}
		if all[i].Class == repo.Remote && all[i].PackageType == repo.P2 {
			m.URL = all[i].URL
		}
		out = append(out, m)
	}
	return out
}

// remoteKeys returns the keys of the P2 remote repositories in all.
func remoteKeys(all []repo.Config) []string {
	var out []string
	for _, k := range all {
		if k.Class == repo.Remote && k.PackageType == repo.P2 {
			out = append(out, k.Key)
		}
	}
	return out
}
