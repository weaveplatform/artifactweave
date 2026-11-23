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

// TestUnitXMLIDSharedAcrossSections: a section the export writes with an
// xs:ID key refuses a key another holds, whether it was stored before or
// arrives in the same patch, and the refusal names both entries.
func TestUnitXMLIDSharedAcrossSections(t *testing.T) {
	for _, tc := range []struct {
		name, stored, patch, want string
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
			name: "layout beside a stored property set", stored: idEntry(idPropSt, "shared"), patch: idEntry(idLayout, "shared"),
			want: "repoLayouts.shared: the ID 'shared' is already used by propertySets.shared",
		},
		{
			name: "property set beside a stored layout", stored: idEntry(idLayout, "shared"), patch: idEntry(idPropSt, "shared"),
			want: "propertySets.shared: the ID 'shared' is already used by repoLayouts.shared",
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
	if _, err := applyWith(t, d, idEntry(idProxy, "x")+idEntry(idLayout, "y"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWith(t, d, "proxies:\n  x: null\n"+idEntry(idBackup, "x")+
		"repoLayouts:\n  y: null\n"+idEntry(idPropSt, "y"), false); err != nil {
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

// TestUnitXMLIDRepositoriesAndLDAPOutsideNamespace: 7.161.26 keeps
// repositories and LDAP connections outside its descriptor, so its
// isKeyAvailable checks neither and its export carries neither. A key one of
// them holds may name any descriptor section, and the reverse, whichever
// surface wrote it and in either order, and the export still carries each ID
// once.
func TestUnitXMLIDRepositoriesAndLDAPOutsideNamespace(t *testing.T) {
	db := inmem.New()
	d := ldapDeps(t, db)
	steps := []func() error{
		func() error {
			_, err := applyWith(t, d, idEntry(idProxy, "dup")+idEntry(idLocal, "both"), false)
			return err
		},
		func() error {
			seedRepo(t, db, repo.Config{Key: "dup-rest", Class: repo.Local, PackageType: repo.Generic})
			ldapConnection(t, d, "dup")
			ldapConnection(t, d, "both")
			return nil
		},
		// A descriptor section over a REST repository, and a repository and an
		// LDAP connection over descriptor sections, in one patch.
		func() error {
			_, err := applyWith(t, d, idEntry(idLayout, "dup-rest")+idEntry(idBackup, "both")+
				"remoteRepositories:\n  dup:\n    type: generic\n    url: https://upstream.invalid\n"+
				idEntry(idLDAP, "dup-rest"), false)
			return err
		},
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatal("step", i, err)
		}
	}
	node, body := exportXML(t, d)
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
	if want := []string{"both", "dup", "dup-rest"}; !slices.Equal(ids, want) {
		t.Fatalf("IDs %v, want %v in\n%s", ids, want, body)
	}
	assertEmptyRepositorySections(t, node)
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
}

// TestUnitXMLIDLDAPGroupNamesAreNotIDs: an ldapGroupSetting's name is
// xs:string (LdapGroupSettingType) and 7.161.26's LdapGroupSetting does not
// mark it @XmlID, so it may repeat any other section's key.
func TestUnitXMLIDLDAPGroupNamesAreNotIDs(t *testing.T) {
	db := inmem.New()
	d := ldapGroupDeps(t, db)
	if _, err := applyWith(t, d, idEntry(idProxy, "devs"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWith(t, d, "security:\n  ldapGroupSettings:\n    devs:\n      enabledLdap: corp\n", false); err != nil {
		t.Fatal(err)
	}
	if _, err := applyWith(t, d, idEntry(idBackup, "ops")+"security:\n  ldapGroupSettings:\n    ops:\n      enabledLdap: corp\n", false); err != nil {
		t.Fatal(err)
	}
	all, err := d.LDAPGroups.GroupSettings(t.Context())
	if err != nil || len(all) != 2 {
		t.Fatal("group settings", all, err)
	}
	if node, body := exportXML(t, d); xmlText(t, node, "proxies/proxy[key=devs]/host") != "proxy.example" {
		t.Fatal(body)
	}
}
