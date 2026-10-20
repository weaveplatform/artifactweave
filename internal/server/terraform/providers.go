package terraform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/idna"

	"github.com/weaveplatform/artifactweave/internal/archive"
	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type providerOp struct {
	name, version, os, arch string
	mirror, list            bool
}

func parseProvider(rest string) (providerOp, bool) {
	p, err := pkgroute.ParsePath(rest)
	if err != nil || p.Folder || len(p.Props) > 0 {
		return providerOp{}, false
	}
	s := p.Segments
	var q providerOp
	var ns, typ string
	switch {
	case len(s) == 5 && s[0] == "providers" && validHostname(s[1]):
		q.mirror = true
		ns, typ = s[2], s[3]
		q.list = s[4] == "index.json"
		if !q.list {
			var ok bool
			q.version, ok = strings.CutSuffix(s[4], ".json")
			if !ok {
				return q, false
			}
		}
	case len(s) == 5 && s[0] == "v1" && s[1] == "providers" && s[4] == "versions":
		ns, typ = s[2], s[3]
		q.list = true
	case len(s) == 8 && s[0] == "v1" && s[1] == "providers" && s[5] == "download":
		ns, typ = s[2], s[3]
		q.version, q.os, q.arch = s[4], s[6], s[7]
	default:
		return q, false
	}
	name, ok := tf.ProviderPackageName(ns, typ)
	if !ok {
		return q, false
	}
	q.name = name
	if !q.list {
		os, arch := q.os, q.arch
		if q.mirror {
			os, arch = "linux", "amd64"
		}
		_, ok = tf.ParseProviderPackageFile(
			q.name + "/" + q.version + "/terraform-provider-" + strings.ToLower(
				typ,
			) + "_" + q.version + "_" + os + "_" + arch + ".zip",
		)
	}
	return q, ok
}

// Mirror hostnames identify the upstream registry, never a remote fetch target.
func validHostname(s string) bool {
	u, err := url.Parse("https://" + s)
	if err != nil || u.User != nil || u.Host != s || u.Path != "" || u.RawQuery != "" ||
		u.Fragment != "" {
		return false
	}
	host, err := idna.Lookup.ToASCII(u.Hostname())
	if err != nil || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

type providerVersion struct {
	version                   string
	archives                  []providerArchive
	sums, signature, manifest types.Item
}
type providerArchive struct {
	item         types.Item
	os, arch, h1 string
}

func (h handler) provider(w http.ResponseWriter, r *http.Request, q providerOp) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.k.Error(w, r, apierror.New(405, "method not allowed"))
		return
	}
	if pkgroute.RequestFrom(r).Repo.TerraformType != repo.TerraformProvider {
		h.k.Error(w, r, missing())
		return
	}
	// Indexed provider paths can have arbitrary folders before the namespace.
	if !h.k.RequireFolder(w, r, "", types.ActionRead) {
		return
	}
	vs, err := h.providerVersions(r, q.name, q.mirror, q.list || q.mirror)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if q.list {
		h.providerList(w, r, q, vs)
		return
	}
	for _, v := range vs {
		if v.version != q.version {
			continue
		}
		if q.mirror {
			h.mirrorVersion(w, r, v)
			return
		}
		for _, a := range v.archives {
			if a.os == q.os && a.arch == q.arch {
				h.providerDownload(w, r, v, a)
				return
			}
		}
	}
	h.k.Error(w, r, missing())
}

// Each item is a snapshot. The metadata hash is used only while it describes
// those exact bytes; URLs sign the snapshots, not a second lookup by path.
func (h handler) providerVersions(
	r *http.Request,
	name string,
	mirror, filter bool,
) ([]providerVersion, error) {
	db, ctx := h.k.DB(), r.Context()
	p, err := db.Packages().Find(ctx, pkgroute.RequestFrom(r).Repo.Key, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, missing()
	}
	if err != nil {
		return nil, fmt.Errorf("terraform: provider: %w", err)
	}
	vs, err := db.Packages().Versions(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("terraform: provider versions: %w", err)
	}
	out := []providerVersion{}
	for _, pv := range vs {
		ids, err := db.Packages().Files(ctx, pv.ID)
		if err != nil {
			return nil, fmt.Errorf("terraform: provider files: %w", err)
		}
		meta := tf.Meta{}
		if len(pv.Metadata) > 0 {
			meta, err = tf.ReadMeta(pv.Metadata)
			if err != nil {
				return nil, fmt.Errorf("terraform: provider metadata: %w", err)
			}
		}
		v := providerVersion{version: pv.Version}
		for _, id := range ids {
			it, err := db.Items().GetByID(ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("terraform: provider item: %w", err)
			}
			f, ok := tf.ParseProviderPackageFile(it.FullPath())
			if !ok || it.Folder ||
				(filter && f.Kind == tf.KindArchive || f.Kind == tf.KindManifest) &&
					!h.k.Can(r, it.FullPath(), types.ActionRead) {
				continue
			}
			switch f.Kind {
			case tf.KindArchive:
				a := providerArchive{item: it, os: f.OS, arch: f.Arch}
				if cached := meta.Archives[f.Platform()]; cached.Path == it.FullPath() &&
					cached.SHA256 == it.Actual.SHA256 {
					a.h1 = cached.H1
				} else if mirror {
					a.h1, err = h.archiveH1(r, it)
					if err != nil {
						return nil, err
					}
				}
				v.archives = append(v.archives, a)
			case tf.KindSums:
				v.sums = it
			case tf.KindSignature:
				v.signature = it
			case tf.KindManifest:
				v.manifest = it
			}
		}
		if len(v.archives) == 0 || !mirror && (v.sums.ID == 0 || v.signature.ID == 0) {
			continue
		}
		slices.SortFunc(
			v.archives,
			func(a, b providerArchive) int { return strings.Compare(a.os+"_"+a.arch, b.os+"_"+b.arch) },
		)
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, missing()
	}
	return out, nil
}

func (h handler) providerList(
	w http.ResponseWriter,
	r *http.Request,
	q providerOp,
	vs []providerVersion,
) {
	if q.mirror {
		versions := map[string]any{}
		for _, v := range vs {
			versions[v.version] = struct{}{}
		}
		writeJSON(w, r, map[string]any{"versions": versions})
		return
	}
	versions := []any{}
	for _, v := range vs {
		protocols, err := h.protocols(r, v.manifest)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		platforms := []map[string]string{}
		for _, a := range v.archives {
			platforms = append(platforms, map[string]string{"os": a.os, "arch": a.arch})
		}
		versions = append(
			versions,
			map[string]any{"version": v.version, "protocols": protocols, "platforms": platforms},
		)
	}
	writeJSON(w, r, map[string]any{"versions": versions})
}

func (h handler) mirrorVersion(w http.ResponseWriter, r *http.Request, v providerVersion) {
	type archive struct {
		URL    string   `json:"url"`
		Hashes []string `json:"hashes,omitempty"`
	}
	archives := map[string]archive{}
	for _, a := range v.archives {
		u, err := h.downloadURL(r, a.item)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		entry := archive{URL: u}
		if a.h1 != "" {
			entry.Hashes = []string{a.h1}
		}
		archives[a.os+"_"+a.arch] = entry
	}
	writeJSON(w, r, map[string]any{"archives": archives})
}

func (h handler) read(r *http.Request, it types.Item) ([]byte, error) {
	b, _, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		return nil, fmt.Errorf("terraform: read %s: %w", it.FullPath(), err)
	}
	defer b.Close()
	data, err := io.ReadAll(b)
	if err != nil {
		return nil, fmt.Errorf("terraform: read %s: %w", it.FullPath(), err)
	}
	return data, nil
}

func (h handler) protocols(r *http.Request, it types.Item) ([]string, error) {
	fallback := []string{"5.0"}
	if it.ID == 0 {
		return fallback, nil
	}
	b, err := h.read(r, it)
	if err != nil {
		return nil, err
	}
	var m struct {
		Metadata struct {
			ProtocolVersions []string `json:"protocol_versions"`
		} `json:"metadata"`
	}
	if json.Unmarshal(b, &m) != nil || len(m.Metadata.ProtocolVersions) == 0 {
		return fallback, nil
	}
	for _, v := range m.Metadata.ProtocolVersions {
		if v != "5.0" && v != "6.0" { // Future numeric protocol versions are valid too.
			parts := strings.Split(v, ".")
			if len(parts) != 2 {
				return fallback, nil
			}
			for _, part := range parts {
				if part == "" || strings.Trim(part, "0123456789") != "" {
					return fallback, nil
				}
			}
		}
	}
	return m.Metadata.ProtocolVersions, nil
}

type publicKey struct {
	ID     string `json:"key_id"`
	Armor  string `json:"ascii_armor"`
	Trust  string `json:"trust_signature"`
	Source string `json:"source"`
	URL    string `json:"source_url"`
}

func (h handler) providerKeys(r *http.Request) ([]publicKey, error) {
	c := pkgroute.RequestFrom(r).Repo
	keys := []publicKey{}
	for i, name := range []string{c.PrimaryKeyPairRef, c.SecondaryKeyPairRef} {
		if name == "" {
			if i == 0 {
				return keys, nil
			}
			continue
		}
		k, err := h.k.DB().Keys().Keypair(r.Context(), name)
		if errors.Is(err, store.ErrNotFound) || err == nil && k.Type != types.KeypairGPG {
			if i == 0 {
				return keys, nil
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("terraform: key: %w", err)
		}
		id, err := tf.KeyID(k.PublicKey)
		if err != nil {
			return nil, apierror.New(409, "Repository GPG public key is invalid")
		}
		keys = append(keys, publicKey{ID: id, Armor: k.PublicKey})
	}
	return keys, nil
}

func (h handler) providerDownload(
	w http.ResponseWriter,
	r *http.Request,
	v providerVersion,
	a providerArchive,
) {
	for _, it := range []types.Item{a.item, v.sums, v.signature} {
		if !h.k.Require(w, r, it.FullPath(), types.ActionRead) {
			return
		}
	}
	keys, err := h.providerKeys(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	armors := []string{}
	for _, k := range keys {
		armors = append(armors, k.Armor)
	}
	sums, err := h.read(r, v.sums)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	sig, err := h.read(r, v.signature)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	verified, err := tf.Check(
		tf.Release{
			Repo:          pkgroute.RequestFrom(r).Repo.Key,
			Archive:       a.item.Name,
			ArchiveSHA256: a.item.Actual.SHA256,
			SumsName:      v.sums.Name,
			Sums:          sums,
			SignatureName: v.signature.Name,
			Signature:     sig,
		},
		armors,
	)
	if err != nil { // Format rejections become this protocol's error envelope.
		h.k.Error(w, r, apierror.New(http.StatusConflict, err.Error()))
		return
	}
	protocols, err := h.protocols(r, v.manifest)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	urls := []string{}
	for _, it := range []types.Item{a.item, v.sums, v.signature} {
		u, err := h.downloadURL(r, it)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		urls = append(urls, u)
	}
	writeJSON(
		w,
		r,
		map[string]any{
			"protocols":             protocols,
			"os":                    a.os,
			"arch":                  a.arch,
			"filename":              a.item.Name,
			"download_url":          urls[0],
			"shasums_url":           urls[1],
			"shasums_signature_url": urls[2],
			"shasum":                verified.Shasum,
			"signing_keys":          map[string]any{"gpg_public_keys": keys},
		},
	)
}

func (h handler) archiveH1(r *http.Request, it types.Item) (string, error) {
	b, info, err := h.k.Blobs().Open(r.Context(), it.Actual.SHA256)
	if err != nil {
		return "", fmt.Errorf("terraform: open archive: %w", err)
	}
	defer b.Close()
	hash, _, err := tf.H1(archive.ReaderAt(b), info.Size)
	if err != nil {
		return "", fmt.Errorf("terraform: archive hash: %w", err)
	}
	return hash, nil
}
