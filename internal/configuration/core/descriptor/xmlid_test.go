package descriptor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

const (
	idProxy  = "proxies:\n  %s:\n    host: proxy.example\n    port: 8080\n"
	idBackup = "backups:\n  %s:\n    cronExp: 0 0 2 * * ?\n"
	idLayout = "repoLayouts:\n  %s:\n    artifactPathPattern: '[org]/[module]/[baseRev]/[module]-[baseRev].[ext]'\n"
	idPropSt = "propertySets:\n  %s:\n    visible: true\n"
	idLocal  = "localRepositories:\n  %s:\n    type: generic\n"
	idLDAP   = "security:\n  ldapSettings:\n    %s:\n      ldapUrl: ldap://h\n      userDnPattern: uid={0}\n"
)

func idEntry(format, key string) string { return strings.ReplaceAll(format, "%s", key) }

// ldapConnection writes a connection as access's LDAP settings API does,
// outside the descriptor.
func ldapConnection(t *testing.T, d Deps, key string) {
	t.Helper()
	url, pattern := "ldap://h", "uid={0}"
	if _, err := d.LDAP.Apply(t.Context(), []accessapi.LDAPChange{{Key: key, URL: &url, UserDNPattern: &pattern}}); err != nil {
		t.Fatal(err)
	}
}

func applyWith(t *testing.T, d Deps, doc string, dryRun bool) (Patch, error) {
	t.Helper()
	p, err := Parse([]byte(doc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(t.Context(), d, p, now, dryRun)
	return *p, err
}

// TestUnitXMLIDSharedAcrossSections: every pair of sections the export writes
// with an xs:ID key refuses a key the other holds, whether it was stored
// before (by the descriptor, the repositories API or access's LDAP API) or
// arrives in the same patch, and the refusal names both entries.
func TestUnitXMLIDSharedAcrossSections(t *testing.T) {
	for _, tc := range []struct {
		name, stored, patch, want string
		rest                      func(*testing.T, *inmem.Store, Deps)
	}{
		{
			name: "backup beside a stored proxy", stored: idEntry(idProxy, "nightly"), patch: idEntry(idBackup, "nightly"),
			want: "backups.nightly: the ID 'nightly' is already used by proxies.nightly",
		},
		{
			name: "proxy and backup in one patch", patch: idEntry(idProxy, "nightly") + idEntry(idBackup, "nightly"),
			want: "proxies.nightly: the ID 'nightly' is already used by backups.nightly",
		},
		{
			name: "layout beside a REST repository",
			rest: func(t *testing.T, db *inmem.Store, _ Deps) {
				t.Helper()
				seedRepo(t, db, repo.Config{Key: "shared", Class: repo.Local, PackageType: repo.Generic})
			},
			patch: idEntry(idLayout, "shared"),
			want:  "repoLayouts.shared: the ID 'shared' is already used by localRepositories.shared",
		},
		{
			name: "repository beside a stored property set", stored: idEntry(idPropSt, "shared"),
			patch: "remoteRepositories:\n  shared:\n    type: generic\n    url: https://upstream.invalid\n",
			want:  "remoteRepositories.shared: the ID 'shared' is already used by propertySets.shared",
		},
		{
			name: "proxy beside an Access LDAP connection",
			rest: func(t *testing.T, _ *inmem.Store, d Deps) {
				t.Helper()
				ldapConnection(t, d, "corp")
			},
			patch: idEntry(idProxy, "corp"),
			want:  "proxies.corp: the ID 'corp' is already used by security.ldapSettings.corp",
		},
		{
			name: "LDAP connection beside a stored repository", stored: idEntry(idLocal, "corp"), patch: idEntry(idLDAP, "corp"),
			want: "security.ldapSettings.corp: the ID 'corp' is already used by localRepositories.corp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := inmem.New()
			d := ldapDeps(t, db)
			if tc.stored != "" {
				if _, err := applyWith(t, d, tc.stored, false); err != nil {
					t.Fatal(err)
				}
			}
			if tc.rest != nil {
				tc.rest(t, db, d)
			}
			_, before := exportXML(t, d)
			for _, dryRun := range []bool{true, false} {
				_, err := applyWith(t, d, tc.patch, dryRun)
				if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), tc.want) {
					t.Fatalf("dry run %t: %v, want %q", dryRun, err, tc.want)
				}
			}
			if _, after := exportXML(t, d); after != before {
				t.Fatalf("a refused patch wrote:\n%s\n%s", before, after)
			}
		})
	}
}

// TestUnitXMLIDReverseProxy: the reverse proxy's key is a field of its one
// document, and POST webServer writes it through Apply, as 7.x's
// updateReverseProxy saves through the XSD-validated descriptor.
func TestUnitXMLIDReverseProxy(t *testing.T) {
	db := inmem.New()
	d := webServerDeps(t, db)
	ws, ctx := NewAPI(d).WebServer, adminCtx(t)
	if _, err := applyWith(t, d, idEntry(idProxy, "nginx"), false); err != nil {
		t.Fatal(err)
	}
	err := ws.SetReverseProxy(ctx, nginx())
	if !errors.Is(err, ErrInvalid) || !strings.HasSuffix(err.Error(), "reverseProxies.nginx: the ID 'nginx' is already used by proxies.nginx") {
		t.Fatal(err)
	}
	if _, err := db.Configs().Get(ctx, configdoc.ConfigReverseProxy, reverseProxyKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("refused reverse proxy stored", err)
	}
	rp := nginx()
	rp.Key = "edge"
	if err := ws.SetReverseProxy(ctx, rp); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWith(t, d, idEntry(idBackup, "edge"), false); !errors.Is(err, ErrInvalid) ||
		!strings.HasSuffix(err.Error(), "backups.edge: the ID 'edge' is already used by reverseProxies.edge") {
		t.Fatal(err)
	}
	// Replacing the reverse proxy frees its old key.
	rp.Key = "front"
	if err := ws.SetReverseProxy(ctx, rp); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWith(t, d, idEntry(idBackup, "edge"), false); err != nil {
		t.Fatal(err)
	}
	// Updating the stored reverse proxy keeps its key: only a new clash is refused.
	if err := ws.SetReverseProxy(ctx, rp); err != nil {
		t.Fatal(err)
	}
}

// TestUnitXMLIDKeysThatLeave: a key the same patch frees, or one an entry
// already held, is not a clash.
func TestUnitXMLIDKeysThatLeave(t *testing.T) {
	db := inmem.New()
	d := ldapDeps(t, db)
	if _, err := applyWith(t, d, idEntry(idProxy, "x")+idEntry(idLocal, "y")+idEntry(idLDAP, "z"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWith(t, d, "proxies:\n  x: null\n"+idEntry(idBackup, "x")+
		"localRepositories:\n  y: null\n"+idEntry(idLayout, "y")+
		"security:\n  ldapSettings:\n    z: null\n"+idEntry(idPropSt, "z"), false); err != nil {
		t.Fatal("keys freed in the same patch", err)
	}
	if _, err := applyWith(t, d, "backups:\n  x:\n    enabled: false\n", false); err != nil {
		t.Fatal("an update of an existing entry", err)
	}
	// A patch that adds no key reads nothing more.
	if _, err := applyWith(t, d, "propertySets:\n  gone: null\n", false); err != nil {
		t.Fatal(err)
	}
}

// TestUnitXMLIDStoredClash: a clash only a REST surface can store does not
// block an unrelated patch, and the export leaves out the repository or LDAP
// connection, which 7.161.26 keeps outside its descriptor.
func TestUnitXMLIDStoredClash(t *testing.T) {
	db := inmem.New()
	d := ldapDeps(t, db)
	if _, err := applyWith(t, d, idEntry(idProxy, "dup")+idEntry(idLocal, "both"), false); err != nil {
		t.Fatal(err)
	}
	seedRepo(t, db, repo.Config{Key: "dup", Class: repo.Local, PackageType: repo.Generic})
	ldapConnection(t, d, "dup")
	ldapConnection(t, d, "both")
	ldapConnection(t, d, "alone")
	if _, err := applyWith(t, d, idEntry(idLayout, "other")+"proxies:\n  dup:\n    port: 8081\n", false); err != nil {
		t.Fatal("an unrelated patch was blocked", err)
	}
	node, body := exportXML(t, d)
	if xmlText(t, node, "proxies/proxy[key=dup]/port") != "8081" {
		t.Fatal(body)
	}
	var ids []string
	for _, section := range node.Children {
		for _, entry := range section.Children {
			for _, sub := range append([]exportNode{entry}, entry.Children...) {
				for _, f := range sub.Children {
					if (f.Name.Local == "key" || f.Name.Local == "name") && (sub.Name.Local != "property") {
						ids = append(ids, f.Text)
					}
				}
			}
		}
	}
	slices.Sort(ids)
	if len(slices.Compact(slices.Clone(ids))) != len(ids) {
		t.Fatalf("duplicate IDs %v in\n%s", ids, body)
	}
	for _, want := range []string{"alone", "both", "dup", "other"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("%s missing from %v", want, ids)
		}
	}
	if strings.Contains(body, "<localRepository><key>dup</key>") || strings.Count(body, "<key>both</key>") != 1 {
		t.Fatal(body)
	}
}

func TestUnitXMLIDReadFailures(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	for _, fail := range []string{"config list", "config get"} {
		p, err := Parse([]byte(idEntry(idBackup, "b")), noEnv)
		if err != nil {
			t.Fatal(err)
		}
		d := depsOf(t, faulty{Store: db, fail: fail})
		if fail == "config get" {
			// Planning reads with Get too; fail only the reverse proxy's read.
			d.Configs = faultyConfigs{db.Configs(), "reverse proxy get"}
		}
		if _, err := Apply(ctx, d, p, now, false); !errors.Is(err, errBoom) || errors.Is(err, ErrInvalid) {
			t.Fatal(fail, err)
		}
	}
	p, err := Parse([]byte(idEntry(idBackup, "b")), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	d := depsOf(t, db)
	d.LDAP = unreadableLDAP{}
	if _, err := Apply(ctx, d, p, now, false); !errors.Is(err, errBoom) {
		t.Fatal(err)
	}
}

// unreadableLDAP fails to list the connections.
type unreadableLDAP struct{ accessapi.LDAPSettings }

func (unreadableLDAP) Connections(context.Context) ([]accessapi.LDAPConnection, error) {
	return nil, errBoom
}
