package virtual_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The store is what the item routes and the storage API read members from.
var _ virtual.Repos = port.RepoStore(nil)

func local(key string, priority bool) repo.Config {
	return repo.Config{Key: key, Class: repo.Local, PriorityResolution: repo.Flex(priority)}
}

func remote(key string, priority bool) repo.Config {
	return repo.Config{Key: key, Class: repo.Remote, PriorityResolution: repo.Flex(priority)}
}

func virt(key string, members ...string) repo.Config {
	return repo.Config{Key: key, Class: repo.Virtual, Repositories: members}
}

func configs(cs ...repo.Config) virtual.Configs {
	out := virtual.Configs{}
	for _, c := range cs {
		out[c.Key] = c
	}
	return out
}

// steps spells members as "key" for a local member or a cache and
// "key^" for an upstream step.
func steps(ms []virtual.Member) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Repo.Key
		if m.Upstream {
			out[i] += "^"
		}
	}
	return strings.Join(out, " ")
}

func TestUnitMembersTranslatedNestedPatterns(t *testing.T) {
	cs := configs(virt("root", "inner", "inner", "other"), virt("inner", "leaf"), virt("other", "inner"), local("leaf", false))
	inner := cs["inner"]
	inner.IncludesPattern = new("ivy/**")
	cs["inner"] = inner
	for _, tc := range []struct{ name, translated, want string }{
		{"admitted", "ivy/org.acme/lib/1.0/lib.jar", "leaf"},
		{"rejected", "maven/org/acme/lib/1.0/lib.jar", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := map[string]int{}
			members, err := virtual.Members(
				t.Context(),
				cs,
				cs["root"],
				virtual.Options{Path: "original", Translate: func(_ context.Context, c repo.Config, path string) (string, error) {
					calls[c.Key]++
					if path != "original" {
						t.Fatalf("hop translation: %q", path)
					}
					return tc.translated, nil
				}},
			)
			if err != nil || steps(members) != tc.want || calls["inner"] != 1 {
				t.Fatal(steps(members), calls, err)
			}
		})
	}
	boom := errors.New("layout storage failed")
	_, err := virtual.Members(
		t.Context(),
		cs,
		cs["root"],
		virtual.Options{Path: "original", Translate: func(context.Context, repo.Config, string) (string, error) { return "", boom }},
	)
	if !errors.Is(err, boom) {
		t.Fatalf("lost translation fault: %v", err)
	}
	// No translator retains the existing storage/package caller contract.
	ms, err := virtual.Members(t.Context(), cs, cs["root"], virtual.Options{Path: "original"})
	if err != nil || len(ms) != 0 {
		t.Fatal(ms, err)
	}
}

func TestUnitMembers(t *testing.T) {
	// Every class in both priority states, listed against R1's group order.
	six := configs(
		remote("r1", false), local("l1", false), remote("pr1", true), local("pl1", true),
		repo.Config{Key: "f1", Class: repo.Federated},
		remote("r2", false), local("pl2", true), remote("pr2", true),
		virt("v", "r1", "l1", "pr1", "pl1", "f1", "r2", "pl2", "pr2"),
	)
	nested := configs(
		local("libs-local", false), local("other-local", false), remote("up", false),
		virt("a", "b", "libs-local"), virt("b", "a", "other-local"),
		virt("loop", "loop"), virt("self", "self", "libs-local"),
		virt("diamond", "left", "right", "libs-local"), virt("left", "shared", "up"),
		virt("right", "shared", "other-local"), virt("shared", "libs-local", "up"),
		virt("gap", "missing", "libs-local", "missing-virtual"),
	)
	excluded := virt("inner", "other-local")
	excluded.ExcludesPattern = new("**/*.bin")
	included := virt("index", "up")
	included.IncludesPattern = new("docs/**/*.txt")
	patterns := configs(
		local("libs-local", false), local("other-local", false), remote("up", false),
		excluded, included,
		virt("outer", "inner", "index", "libs-local"),
		virt("again", "inner", "outer"),
	)
	for _, tc := range []struct {
		name string
		rs   virtual.Configs
		top  string
		o    virtual.Options
		want string
	}{
		{"six groups", six, "v", virtual.Options{}, "pl1 pl2 pr1 pr2 pr1^ pr2^ l1 f1 r1 r2 r1^ r2^"},
		{
			"package keeps each remote once", six, "v",
			virtual.Options{Order: virtual.Package},
			"pl1 pl2 pr1^ pr2^ l1 f1 r1^ r2^",
		},
		{
			"package without upstreams keeps the caches", six, "v",
			virtual.Options{Order: virtual.Package, NoUpstream: true},
			"pl1 pl2 pr1 pr2 l1 f1 r1 r2",
		},
		{"stored ignores priority", six, "v", virtual.Options{Order: virtual.Stored}, "l1 pl1 f1 pl2 r1 pr1 r2 pr2"},
		{"no upstream", six, "v", virtual.Options{NoUpstream: true}, "pl1 pl2 pr1 pr2 l1 f1 r1 r2"},
		{"no priority", six, "v", virtual.Options{NoPriority: true}, "l1 pl1 f1 pl2 r1 pr1 r2 pr2 r1^ pr1^ r2^ pr2^"},
		{"nested cycle", nested, "a", virtual.Options{}, "other-local libs-local"},
		{"nested cycle from the other side", nested, "b", virtual.Options{}, "libs-local other-local"},
		{"cycle only", nested, "loop", virtual.Options{}, ""},
		{"self member", nested, "self", virtual.Options{}, "libs-local"},
		{"diamond keeps first positions", nested, "diamond", virtual.Options{}, "libs-local other-local up up^"},
		{"missing keys", nested, "gap", virtual.Options{}, "libs-local"},
		{
			"package without remotes keeps nothing to drop", nested, "a",
			virtual.Options{Order: virtual.Package},
			"other-local libs-local",
		},
		{"nested patterns drop a virtual", patterns, "outer", virtual.Options{Path: "a/f.bin"}, "libs-local"},
		{"nested patterns admit a virtual", patterns, "outer", virtual.Options{Path: "docs/f.txt"}, "other-local libs-local up up^"},
		{"a folder leading to an include", patterns, "outer", virtual.Options{Path: "docs", Folder: true}, "other-local libs-local up up^"},
		{"a file is not a folder", patterns, "outer", virtual.Options{Path: "docs"}, "other-local libs-local"},
		{"a system path passes the patterns", patterns, "outer", virtual.Options{Path: "p2.index"}, "other-local libs-local up up^"},
		{"no path checks no patterns", patterns, "outer", virtual.Options{Order: virtual.Stored}, "other-local libs-local up"},
		{"a dropped virtual stays dropped", patterns, "again", virtual.Options{Path: "a/f.bin"}, "libs-local"},
		{"the top virtual's own patterns are the caller's", patterns, "inner", virtual.Options{Path: "a/f.bin"}, "other-local"},
		{"a real repository has no members", nested, "libs-local", virtual.Options{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := virtual.Members(t.Context(), tc.rs, tc.rs[tc.top], tc.o)
			if err != nil {
				t.Fatal(err)
			}
			if s := steps(got); s != tc.want {
				t.Errorf("members %q, want %q", s, tc.want)
			}
		})
	}
}

var errStore = errors.New("store down")

// failing fails the read of one key.
type failing struct {
	virtual.Configs
	key string
}

func (f failing) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == f.key {
		return repo.Config{}, errStore
	}
	return f.Configs.Get(ctx, key)
}

func TestUnitMembersStoreError(t *testing.T) {
	rs := configs(
		local("libs-local", false),
		virt("inner", "broken"),
		virt("outer", "libs-local", "inner"),
	)
	_, err := virtual.Members(t.Context(), failing{rs, "broken"}, rs["outer"], virtual.Options{})
	if !errors.Is(err, errStore) || !strings.Contains(err.Error(), "member broken of inner") {
		t.Fatalf("error %v", err)
	}
	if err := virtual.Walk(t.Context(), failing{rs, "broken"}, rs["outer"],
		func([]repo.Config, repo.Config) error { return nil }); !errors.Is(err, errStore) {
		t.Fatalf("walk error %v", err)
	}
}

func TestUnitConfigsGet(t *testing.T) {
	rs := configs(local("libs-local", false))
	if c, err := rs.Get(t.Context(), "libs-local"); err != nil || c.Key != "libs-local" {
		t.Fatalf("get %+v %v", c, err)
	}
	if _, err := rs.Get(t.Context(), "missing"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
}

// route spells a visit as its chain's keys and the member, "a/b>m".
func route(chain []repo.Config, m repo.Config) string {
	keys := make([]string, len(chain))
	for i, c := range chain {
		keys[i] = c.Key
	}
	return strings.Join(keys, "/") + ">" + m.Key
}

func TestUnitWalk(t *testing.T) {
	rs := configs(
		local("libs-local", false), local("other-local", false), remote("up", false),
		virt("a", "b", "libs-local"), virt("b", "a", "other-local", "b"),
		virt("diamond", "left", "right", "missing"), virt("left", "shared"),
		virt("right", "shared", "up"), virt("shared", "libs-local", "diamond"),
		virt("loop", "loop"),
	)
	for _, tc := range []struct {
		top  string
		want []string
	}{
		// A cycle ends the route that repeats a virtual, and a
		// self-member is skipped.
		{"a", []string{"a>b", "a/b>other-local", "a>libs-local"}},
		// A diamond keeps both routes to each member, and a missing key
		// is skipped.
		{"diamond", []string{
			"diamond>left", "diamond/left>shared", "diamond/left/shared>libs-local",
			"diamond>right", "diamond/right>shared", "diamond/right/shared>libs-local",
			"diamond/right>up",
		}},
		{"loop", nil},
	} {
		var got []string
		var grown [][]repo.Config
		err := virtual.Walk(
			t.Context(),
			rs,
			rs[tc.top],
			func(chain []repo.Config, m repo.Config) error {
				got = append(got, route(chain, m))
				// An append to chain must not reach the route Walk goes on
				// with, which the later routes in got would show.
				grown = append(grown, append(chain, repo.Config{Key: "clobbered"}))
				return nil
			},
		)
		if err != nil || !slices.Equal(got, tc.want) || len(grown) != len(tc.want) {
			t.Errorf("%s: %q %v, want %q", tc.top, got, err, tc.want)
		}
	}
	// A visit's error stops the walk.
	visits := 0
	stop := errors.New("bound")
	err := virtual.Walk(t.Context(), rs, rs["diamond"], func([]repo.Config, repo.Config) error {
		visits++
		if visits == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || visits != 2 {
		t.Fatalf("stopped after %d: %v", visits, err)
	}
	// The error of a nested virtual's member stops the walk too.
	visits = 0
	err = virtual.Walk(t.Context(), rs, rs["diamond"], func(_ []repo.Config, m repo.Config) error {
		visits++
		if m.Key == "libs-local" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || visits != 3 {
		t.Fatalf("nested stop after %d: %v", visits, err)
	}
}

// tracedRepos records actual store calls, including reads later suppressed by
// effective-member deduplication. Route-local cycles skip before that boundary.
type tracedRepos struct {
	virtual.Repos
	events *[]string
}

func (r tracedRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	*r.events = append(*r.events, "read "+key)
	return r.Repos.Get(ctx, key)
}

func TestUnitTraversalPreservesMemberAndRouteLookupOrder(t *testing.T) {
	cs := configs(
		virt("root", "left", "right", "leaf", "absent"),
		virt("left", "shared", "root"), virt("right", "shared", "leaf"),
		virt("shared", "leaf", "up"), local("leaf", false), remote("up", false),
	)
	for _, tc := range []struct {
		name, wantMembers     string
		wantReads, wantRoutes []string
	}{
		{
			name: "effective members", wantMembers: "leaf up",
			wantReads: []string{
				"read left", "read shared", "read leaf", "read up", "read root",
				"read right", "read shared", "read leaf", "read leaf", "read absent",
			},
		},
		{
			name: "every route",
			wantReads: []string{
				"read left", "read shared", "read leaf", "read up", "read right",
				"read shared", "read leaf", "read up", "read leaf", "read leaf", "read absent",
			},
			wantRoutes: []string{
				"root>left", "root/left>shared", "root/left/shared>leaf", "root/left/shared>up",
				"root>right", "root/right>shared", "root/right/shared>leaf", "root/right/shared>up",
				"root/right>leaf", "root>leaf",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events, routes []string
			rs := tracedRepos{Repos: cs, events: &events}
			var err error
			if tc.wantMembers != "" {
				var members []virtual.Member
				members, err = virtual.Members(t.Context(), rs, cs["root"], virtual.Options{Order: virtual.Stored})
				if steps(members) != tc.wantMembers {
					t.Fatal("effective members", steps(members), tc.wantMembers)
				}
			} else {
				err = virtual.Walk(t.Context(), rs, cs["root"], func(chain []repo.Config, m repo.Config) error {
					routes = append(routes, route(chain, m))
					return nil
				})
			}
			if err != nil || !slices.Equal(events, tc.wantReads) || !slices.Equal(routes, tc.wantRoutes) {
				t.Fatal("trace", events, routes, err, "want", tc.wantReads, tc.wantRoutes)
			}
		})
	}
}

func TestUnitTraversalPreservesCycleLookupFault(t *testing.T) {
	cs := configs(virt("root", "root", "leaf"), local("leaf", false))
	for _, name := range []string{"effective members", "every route"} {
		t.Run(name, func(t *testing.T) {
			var events []string
			rs := tracedRepos{Repos: failing{cs, "root"}, events: &events}
			if name == "effective members" {
				members, err := virtual.Members(t.Context(), rs, cs["root"], virtual.Options{})
				if !errors.Is(err, errStore) || members != nil || !slices.Equal(events, []string{"read root"}) {
					t.Fatal("cycle configuration read must precede global dedup", members, events, err)
				}
				return
			}
			err := virtual.Walk(t.Context(), rs, cs["root"], func(chain []repo.Config, m repo.Config) error {
				events = append(events, "visit "+route(chain, m))
				return nil
			})
			if err != nil || !slices.Equal(events, []string{"read leaf", "visit root>leaf"}) {
				t.Fatal("route cycle must stop before its store read", events, err)
			}
		})
	}
}

func TestUnitTraversalStopsBeforeLaterReads(t *testing.T) {
	cs := configs(virt("root", "inner", "later"), virt("inner", "leaf", "broken"),
		local("leaf", false), local("later", false))
	stop := errors.New("stop traversal")
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"translation", []string{"read inner", "translate inner original"}},
		{"member store", []string{"read inner", "read leaf", "read broken"}},
		{"route callback", []string{"read inner", "visit root>inner"}},
		{"route store", []string{"read inner", "visit root>inner", "read leaf", "visit root/inner>leaf", "read broken"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			rs := tracedRepos{Repos: failing{cs, "broken"}, events: &events}
			wantErr := errStore
			var err error
			switch tc.name {
			case "translation":
				wantErr = stop
				_, err = virtual.Members(t.Context(), rs, cs["root"], virtual.Options{
					Path: "original",
					Translate: func(_ context.Context, m repo.Config, path string) (string, error) {
						events = append(events, "translate "+m.Key+" "+path)
						return "", stop
					},
				})
			case "member store":
				_, err = virtual.Members(t.Context(), rs, cs["root"], virtual.Options{})
			default:
				if tc.name == "route callback" {
					wantErr = stop
				}
				err = virtual.Walk(t.Context(), rs, cs["root"], func(chain []repo.Config, m repo.Config) error {
					events = append(events, "visit "+route(chain, m))
					if tc.name == "route callback" {
						return stop
					}
					return nil
				})
			}
			if !errors.Is(err, wantErr) || !slices.Equal(events, tc.want) {
				t.Fatal("first failure", events, err, "want", tc.want, wantErr)
			}
		})
	}
}

// TestUnitAnswer: a walk that found nothing answers as 7.161.26's
// DownloadServiceImpl.respondResourceNotFound answers the unfound resource
// processStandard returns: 404 without a refusal, the first refusal's 403,
// 404 "Resource not found" when the virtual or the security configuration
// hides refusals, and the 401 challenge to anonymous when nothing hides
// them; each with the virtual's path. A walk that found the item has no
// answer.
func TestUnitAnswer(t *testing.T) {
	open := virt("virt")
	hiding := virt("virt")
	hiding.HideUnauthorizedResources = new(bool)
	*hiding.HideUnauthorizedResources = true
	unhiding := virt("virt")
	unhiding.HideUnauthorizedResources = new(bool)
	var refused virtual.Resolution
	if !refused.Deny(virtual.Denial{Member: "a-local", Path: "d/x.bin", User: "bob"}) ||
		refused.Deny(virtual.Denial{Member: "b-local", Path: "d/x.bin", User: "bob"}) {
		t.Fatal("Deny keeps the first refusal alone")
	}
	const (
		suffix    = "; Path: 'virt:d/x.bin'"
		forbidden = "Download request for repo:path 'a-local:d/x.bin' is forbidden for user: 'bob'."
	)
	for _, tc := range []struct {
		name       string
		v          repo.Config
		r          virtual.Resolution
		anonymous  bool
		hideGlobal bool
		status     int
		message    string
	}{
		{"found", open, virtual.Resolution{Found: true}, false, false, 0, ""},
		{"miss", open, virtual.Resolution{}, false, false, 404, "Could not find resource" + suffix},
		{"miss, hidden", hiding, virtual.Resolution{}, true, true, 404, "Could not find resource" + suffix},
		{"refused", open, refused, false, false, 403, forbidden + suffix},
		{"refused, flag off", unhiding, refused, false, false, 403, forbidden + suffix},
		{"refused, hidden", hiding, refused, false, false, 404, "Resource not found" + suffix},
		{"refused, hidden globally", open, refused, false, true, 404, "Resource not found" + suffix},
		{"anonymous refused, hidden", hiding, refused, true, false, 404, "Resource not found" + suffix},
		{"anonymous refused", open, refused, true, false, 401, "Unauthorized"},
	} {
		err := virtual.Answer(tc.v, "d/x.bin", tc.r, tc.anonymous, tc.hideGlobal)
		var e *apierror.Error
		switch {
		case tc.status == 0 && err != nil:
			t.Errorf("%s: %v, want none", tc.name, err)
		case tc.status != 0 && (!errors.As(err, &e) || e.Status != tc.status || e.Message != tc.message):
			t.Errorf("%s: %v, want %d %q", tc.name, err, tc.status, tc.message)
		}
	}
}

// TestUnitAnswerCarriesTheHiddenAnswer (#1903): a refusal Answer does not
// hide itself is an apierror.Hideable whose Hidden answer is the 404,
// which a request gives while the global hideUnauthorizedResources is on
// (httpkit.Hidden); a miss and an answer already hidden carry none.
func TestUnitAnswerCarriesTheHiddenAnswer(t *testing.T) {
	var refused virtual.Resolution
	refused.Deny(virtual.Denial{Member: "a-local", Path: "d/x.bin", User: "bob"})
	for _, anonymous := range []bool{false, true} {
		var h *apierror.Hideable
		if err := virtual.Answer(virt("virt"), "d/x.bin", refused, anonymous, false); !errors.As(err, &h) ||
			h.Hidden.Status != http.StatusNotFound || h.Hidden.Message != "Resource not found; Path: 'virt:d/x.bin'" {
			t.Errorf("anonymous %t: %v", anonymous, err)
		}
	}
	for name, err := range map[string]error{
		"miss":   virtual.Answer(virt("virt"), "d/x.bin", virtual.Resolution{}, false, false),
		"hidden": virtual.Answer(virt("virt"), "d/x.bin", refused, false, true),
	} {
		var h *apierror.Hideable
		if errors.As(err, &h) {
			t.Errorf("%s: %v carries a hidden answer", name, err)
		}
	}
}
