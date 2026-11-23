package descriptor

import (
	"context"
	"fmt"
	"maps"
	"slices"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// sectionLDAP names the LDAP connections in results and refusals.
const sectionLDAP = SectionSecurity + ".ldapSettings"

// idSections are the keyed sections stored as configuration documents whose
// key is an xs:ID in 7.161.26's artifactory.xsd (ProxyType, BackupType,
// PropertySetType, RepoLayoutType), in the order a refusal prefers to name
// the entry already holding a key. The repositories (RepoType), the reverse
// proxy (ReverseProxyType) and the LDAP connections (LdapSetting's @XmlID)
// complete the set the export writes.
var idSections = []struct {
	name string
	kind configdoc.ConfigKind
}{
	{SectionProxies, configdoc.ConfigProxy},
	{SectionBackups, configdoc.ConfigBackup},
	{SectionPropertySets, configdoc.ConfigPropertySet},
	{SectionLayouts, configdoc.ConfigLayout},
}

// repoKeys are the classes of the repositories, by key, before and after a
// patch.
type repoKeys struct {
	before, after map[string]repo.Class
}

// xmlIDs holds the sections using each key as an XML ID, in the order they
// were added.
type xmlIDs map[string][]string

func (ids xmlIDs) add(section, key string) { ids[key] = append(ids[key], section) }

// checkXMLIDs refuses a patch that leaves a key used by two sections when it
// was not before. An XML document's IDs share one namespace, so the export
// would otherwise hold two elements with one ID, which an XSD-validating
// reader refuses: 7.161.26 validates its descriptor against the XSD before
// every save (CentralConfigServiceImpl.validateDescriptor). A clash already
// stored is not this patch's doing, since REST surfaces that 7.x keeps
// outside its descriptor can make one, so it does not block an unrelated
// patch; the export leaves it out instead (dropDuplicateIDs).
func checkXMLIDs(
	ctx context.Context,
	d Deps,
	repos repoKeys,
	configs []configOp,
	ldap []accessapi.LDAPResult,
) error {
	if !mayAddID(repos, configs, ldap) {
		return nil
	}
	before, after := xmlIDs{}, xmlIDs{}
	for _, s := range repoSections {
		for _, key := range slices.Sorted(maps.Keys(repos.before)) {
			if repos.before[key] == s.class {
				before.add(s.name, key)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(repos.after)) {
			if repos.after[key] == s.class {
				after.add(s.name, key)
			}
		}
	}
	for _, s := range idSections {
		docs, err := d.Configs.List(ctx, s.kind)
		if err != nil {
			return fmt.Errorf("list %s: %w", s.name, err)
		}
		keys := map[string]bool{}
		for _, doc := range docs {
			before.add(s.name, doc.Key)
			keys[doc.Key] = true
		}
		for _, op := range configs {
			if op.doc.Kind == s.kind {
				keys[op.doc.Key] = !op.shouldDelete
			}
		}
		for _, key := range slices.Sorted(maps.Keys(keys)) {
			if keys[key] {
				after.add(s.name, key)
			}
		}
	}
	if err := reverseProxyIDs(ctx, d, configs, before, after); err != nil {
		return err
	}
	if err := ldapIDs(ctx, d, ldap, before, after); err != nil {
		return err
	}
	return introducedID(before, after)
}

// mayAddID reports whether the patch creates a repository or an LDAP
// connection, or writes a keyed configuration document, which may add a key;
// a patch that does none of these leaves every key as it was.
func mayAddID(repos repoKeys, configs []configOp, ldap []accessapi.LDAPResult) bool {
	for key := range repos.after {
		if _, ok := repos.before[key]; !ok {
			return true
		}
	}
	for _, op := range configs {
		if op.shouldDelete {
			continue
		}
		if op.doc.Kind == configdoc.ConfigReverseProxy {
			return true
		}
		for _, s := range idSections {
			if op.doc.Kind == s.kind {
				return true
			}
		}
	}
	for _, r := range ldap {
		if r.Outcome == accessapi.LDAPCreated {
			return true
		}
	}
	return false
}

// reverseProxyIDs adds the reverse proxy's key, which is a field of its one
// stored document rather than the document's key.
func reverseProxyIDs(ctx context.Context, d Deps, configs []configOp, before, after xmlIDs) error {
	rp, stored, err := readReverseProxy(ctx, d.Configs)
	if err != nil {
		return err
	}
	key := ""
	if stored {
		key = rp.Key
		before.add(SectionReverseProxies, key)
	}
	for _, op := range configs {
		if op.doc.Kind != configdoc.ConfigReverseProxy {
			continue
		}
		key = ""
		if op.shouldDelete {
			continue
		}
		fields, err := publicDocument(op.doc)
		if err != nil {
			return err
		}
		written, err := system.ReverseProxyFromDocument(fields)
		if err != nil {
			return fmt.Errorf("%s: %w", SectionReverseProxies, err)
		}
		key = written.Key
	}
	if key != "" {
		after.add(SectionReverseProxies, key)
	}
	return nil
}

// ldapIDs adds the LDAP connections, which access stores. Without an LDAP
// binding the patch names none and the export has none.
func ldapIDs(ctx context.Context, d Deps, ldap []accessapi.LDAPResult, before, after xmlIDs) error {
	if d.LDAP == nil {
		return nil
	}
	all, err := d.LDAP.Connections(ctx)
	if err != nil {
		return fmt.Errorf("read %s: %w", sectionLDAP, err)
	}
	keys := map[string]bool{}
	for _, c := range all {
		before.add(sectionLDAP, c.Key)
		keys[c.Key] = true
	}
	for _, r := range ldap {
		switch r.Outcome {
		case accessapi.LDAPCreated:
			keys[r.Key] = true
		case accessapi.LDAPDeleted:
			keys[r.Key] = false
		}
	}
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		if keys[key] {
			after.add(sectionLDAP, key)
		}
	}
	return nil
}

// introducedID names the first key, in sorted order, that after gives to a
// section which did not hold it before while another section holds it, and
// that other section. The text extends the one 7.x's UniqueXmlIdValidator
// answers ("The ID '%s' is already used.") with the entry that holds the key.
func introducedID(before, after xmlIDs) error {
	for _, key := range slices.Sorted(maps.Keys(after)) {
		sections := after[key]
		if len(sections) < 2 {
			continue
		}
		for _, s := range sections {
			if slices.Contains(before[key], s) {
				continue
			}
			// Name the section that already held the key when there is one,
			// so a refusal points at the stored entry, not a sibling in the
			// same patch.
			others := slices.DeleteFunc(slices.Clone(sections), func(o string) bool { return o == s })
			other := others[0]
			for _, o := range others {
				if slices.Contains(before[key], o) {
					other = o
					break
				}
			}
			return fmt.Errorf("%w: %s.%s: the ID '%s' is already used by %s.%s", ErrInvalid, s, key, key, other, key)
		}
	}
	return nil
}

// dropDuplicateIDs keeps the export one namespace of IDs when the store holds
// a key in two sections, which only a REST surface outside the descriptor can
// make: the repositories API, or access's LDAP settings API. 7.161.26 keeps
// both outside its descriptor (repositories in its repository configuration
// store, LDAP connections in Access), so its export carries neither and their
// REST APIs check a key only against their own kind. The sections 7.x's
// descriptor holds therefore keep the ID, a repository sharing it is left
// out, and an LDAP connection sharing a key with either is left out too.
// References to a left-out repository keep their key; they then resolve to
// the element that holds it.
func dropDuplicateIDs(out *exportDocument) {
	used := map[string]bool{}
	for _, section := range [][]exportFields{out.Proxies, out.ReverseProxies, out.Backups, out.PropertySets, out.Layouts} {
		for _, e := range section {
			used[exportID(e)] = true
		}
	}
	keep := func(entries []exportFields) []exportFields {
		return slices.DeleteFunc(entries, func(e exportFields) bool { return used[exportID(e)] })
	}
	out.Local, out.Remote, out.Virtual, out.Federated = keep(out.Local), keep(out.Remote), keep(out.Virtual), keep(out.Federated)
	for _, section := range [][]exportFields{out.Local, out.Remote, out.Virtual, out.Federated} {
		for _, e := range section {
			used[exportID(e)] = true
		}
	}
	out.Security.LDAP = keep(out.Security.LDAP)
}

// exportID is the text of an entry's key or name element, its XML ID.
func exportID(e exportFields) string {
	for _, f := range e.Fields {
		if f.Name == "key" || f.Name == "name" {
			return f.Text
		}
	}
	return ""
}
