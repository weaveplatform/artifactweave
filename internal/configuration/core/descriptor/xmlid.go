package descriptor

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// idSections are the keyed sections stored as configuration documents whose
// key is an xs:ID in 7.161.26's artifactory.xsd (ProxyType, BackupType,
// PropertySetType, RepoLayoutType), in the order a refusal prefers to name
// the entry already holding a key. The reverse proxy (ReverseProxyType)
// completes the set the export writes. Repositories and LDAP connections are
// not in it: 7.161.26 keeps them outside its descriptor, in its repository
// configuration store and in Access, so neither the export nor 7.x's
// isKeyAvailable holds their keys (issue 1893).
var idSections = []struct {
	name string
	kind configdoc.ConfigKind
}{
	{SectionProxies, configdoc.ConfigProxy},
	{SectionBackups, configdoc.ConfigBackup},
	{SectionPropertySets, configdoc.ConfigPropertySet},
	{SectionLayouts, configdoc.ConfigLayout},
}

// xmlIDs holds the sections using each key as an XML ID, in the order they
// were added.
type xmlIDs map[string][]string

func (ids xmlIDs) add(section, key string) { ids[key] = append(ids[key], section) }

// checkXMLIDs refuses a patch that leaves a key used by two sections when it
// was not before. An XML document's IDs share one namespace, so the export
// would otherwise hold two elements with one ID, which an XSD-validating
// reader refuses: 7.161.26 validates its descriptor against the XSD before
// every save (CentralConfigServiceImpl.validateDescriptor). Every section in
// the namespace is written only through this check (PATCH, config apply, the
// bootstrap descriptor, POST webServer) or is a bundled layout, so the export
// never carries one ID twice.
func checkXMLIDs(ctx context.Context, d Deps, configs []configOp) error {
	if !mayAddID(configs) {
		return nil
	}
	before, after := xmlIDs{}, xmlIDs{}
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
	return introducedID(before, after)
}

// mayAddID reports whether the patch writes a keyed configuration document,
// which may add a key; a patch that writes none leaves every key as it was.
func mayAddID(configs []configOp) bool {
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
