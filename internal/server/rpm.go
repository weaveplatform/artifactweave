package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/format/rpm"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const rpmMetadataLimit = 36 << 20

var (
	errRPMVirtualKey       = errors.New("rpm: virtual signing key must be GPG")
	errRPMVirtualCycle     = errors.New("rpm: virtual repository cycle")
	errRPMMemberType       = errors.New("rpm: incompatible virtual member")
	errRPMMetadataTooLarge = errors.New("rpm: metadata too large")
)

type rpmCandidate struct {
	repo repo.Config
	pkg  rpm.Package
}

func (s artifactoryService) tryRPMItem(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	cached bool, req itemRequest, p auth.Principal, acc access,
) bool {
	if c.PackageType != repo.RPM || c.Class != repo.Virtual || cached || req.folder {
		return false
	}
	if strings.Contains(req.path, "repodata/") {
		s.serveVirtualRPMMetadata(w, r, st, c, req, p, acc)
		return true
	}
	if strings.HasSuffix(req.path, ".rpm") {
		s.serveVirtualRPMPackage(w, r, st, c, req, p, acc)
		return true
	}
	return false
}

func (s artifactoryService) serveVirtualRPMMetadata(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	req itemRequest, p auth.Principal, acc access,
) {
	root, name, ok := strings.Cut(req.path, "repodata/")
	if !ok || name == "" || strings.Contains(name, "/") || len(req.props) != 0 ||
		r.URL.RawQuery != "" {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.folder(c, req.path, types.ActionRead) || !acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	candidates, err := s.rpmCandidates(r.Context(), st, c, root, p, acc, map[string]bool{})
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "RPM virtual members", err))
		return
	}
	packages := make([]rpm.Package, 0, len(candidates))
	for _, candidate := range candidates {
		packages = append(packages, candidate.pkg)
	}
	meta, err := rpm.EncodeMetadata(packages, root, time.Unix(0, 0))
	if err != nil {
		writeErr(w, r, s.internalError(r.Context(), "RPM virtual metadata", err))
		return
	}
	if err := signVirtualRPM(r.Context(), st, c, meta.Files); err != nil {
		writeErr(w, r, s.internalError(r.Context(), "RPM virtual signature", err))
		return
	}
	body, ok := meta.Files["repodata/"+name]
	if !ok {
		writeErr(w, r, itemNotFound())
		return
	}
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, name, time.Time{}, bytes.NewReader(body))
	_ = sw.complete(r)
}

func signVirtualRPM(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	files map[string][]byte,
) error {
	if c.KeyPair == "" {
		return nil
	}
	pair, err := st.Keys().Keypair(ctx, c.KeyPair)
	if err != nil {
		return fmt.Errorf("rpm: virtual signing key: %w", err)
	}
	if pair.Type != types.KeypairGPG {
		return errRPMVirtualKey
	}
	sig, err := rpm.SignRepomd(files["repodata/repomd.xml"], pair.PrivateKey, pair.Passphrase)
	if err != nil {
		return fmt.Errorf("rpm: sign virtual repomd: %w", err)
	}
	files["repodata/repomd.xml.asc"] = sig
	files["repodata/repomd.xml.key"] = []byte(pair.PublicKey)
	return nil
}

func (s artifactoryService) serveVirtualRPMPackage(
	w http.ResponseWriter, r *http.Request, st store.Store, c repo.Config,
	req itemRequest, p auth.Principal, acc access,
) {
	if len(req.props) != 0 || r.URL.RawQuery != "" {
		writeErr(w, r, itemNotFound())
		return
	}
	if !acc.folder(c, req.path, types.ActionRead) || !acc.file(c, req.path, types.ActionRead) ||
		generic.CheckDownload(c, req.path, false) != nil {
		writeErr(w, r, denied(p))
		return
	}
	root := ""
	if c.YumRootDepth != nil && *c.YumRootDepth > 0 {
		parts := strings.Split(req.path, "/")
		if len(parts) <= *c.YumRootDepth {
			writeErr(w, r, itemNotFound())
			return
		}
		root = strings.Join(parts[:*c.YumRootDepth], "/") + "/"
	}
	candidates, err := s.rpmCandidates(r.Context(), st, c, root, p, acc, map[string]bool{})
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "RPM virtual members", err))
		return
	}
	for _, candidate := range candidates {
		if candidate.pkg.Path != req.path {
			continue
		}
		if candidate.repo.Class == repo.Remote {
			s.serveVirtualRemoteRPMPackage(w, r, candidate.repo, req, candidate.pkg.SHA256, p, acc)
			return
		}
		it, err := st.Items().Get(r.Context(), candidate.repo.Key, false, req.path)
		if err != nil {
			writeErr(w, r, s.itemError(r.Context(), "RPM virtual package", err))
			return
		}
		if it.Folder || it.Actual.SHA256 != candidate.pkg.SHA256 {
			writeErr(w, r, itemNotFound())
			return
		}
		s.serveFile(w, r, st, found{repo: candidate.repo, item: it}, p)
		return
	}
	writeErr(w, r, itemNotFound())
}

func (s artifactoryService) serveVirtualRemoteRPMPackage(
	w http.ResponseWriter, r *http.Request, c repo.Config, req itemRequest,
	wantSHA string, p auth.Principal, acc access,
) {
	if s.cfg.RemoteSources == nil {
		writeErr(w, r, itemNotFound())
		return
	}
	q := source.Request{
		Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
		Resource: source.Resource{Kind: source.Artifact, Path: req.path, Method: r.Method},
	}
	q, err := rpmSourceRequest(q)
	if err != nil {
		writeErr(w, r, s.itemError(r.Context(), "RPM source", err))
		return
	}
	validate := q.Resource.Validate
	q.Resource.Variant = "rpm-virtual-package-v1-" + wantSHA
	q.Resource.Validate = func(ctx context.Context, body io.ReadSeeker, size int64) error {
		if err := validate(ctx, body, size); err != nil {
			return err
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return rpmSourceIntegrity()
		}
		h := sha256.New()
		read, err := io.Copy(h, io.LimitReader(body, size))
		if err != nil || read != size || fmt.Sprintf("%x", h.Sum(nil)) != wantSHA {
			return rpmSourceIntegrity()
		}
		return nil
	}
	result, err := s.cfg.RemoteSources.Read(r.Context(), q)
	if err != nil {
		writeErr(
			w,
			r,
			s.itemError(r.Context(), "RPM source", pkgroute.SourceError(err, p.Anonymous)),
		)
		return
	}
	if result.Stream != nil {
		s.serveRemoteStream(w, r, result, c, req, q.Resource, "", p.Anonymous)
		return
	}
	if !s.remoteDeliveryAllowed(w, r, result, p.Anonymous) {
		return
	}
	if result.Item == nil || result.Item.Actual.SHA256 != wantSHA {
		writeErr(w, r, pkgroute.SourceError(rpmSourceIntegrity(), p.Anonymous))
		return
	}
	s.serveFile(w, r, s.cfg.Store, found{repo: c, item: *result.Item, used: result.Used}, p)
}

func (s artifactoryService) rpmCandidates(
	ctx context.Context, st store.Store, c repo.Config, root string,
	p auth.Principal, acc access, seen map[string]bool,
) ([]rpmCandidate, error) {
	if seen[c.Key] || len(seen) >= 8 {
		return nil, errRPMVirtualCycle
	}
	seen[c.Key] = true
	defer delete(seen, c.Key)
	selected := map[string]bool{}
	var out []rpmCandidate
	indexPath := root + "repodata/repomd.xml"
	for _, key := range c.Repositories {
		member, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("rpm: member %s: %w", key, err)
		}
		if member.PackageType != repo.RPM {
			return nil, fmt.Errorf("%w: %s", errRPMMemberType, key)
		}
		if !acc.folder(member, indexPath, types.ActionRead) ||
			!acc.file(member, indexPath, types.ActionRead) ||
			generic.CheckDownload(member, indexPath, false) != nil {
			continue
		}
		var entries []rpmCandidate
		if member.Class == repo.Virtual {
			entries, err = s.rpmCandidates(ctx, st, member, root, p, acc, seen)
		} else {
			var packages []rpm.Package
			packages, err = s.rpmMemberPackages(ctx, st, member, root, p, acc)
			for _, pkg := range packages {
				entries = append(entries, rpmCandidate{repo: member, pkg: pkg})
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("rpm: member %s index: %w", key, err)
		}
		for _, entry := range entries {
			if selected[entry.pkg.Path] || !acc.file(c, entry.pkg.Path, types.ActionRead) ||
				!acc.file(entry.repo, entry.pkg.Path, types.ActionRead) ||
				generic.CheckDownload(c, entry.pkg.Path, false) != nil ||
				generic.CheckDownload(entry.repo, entry.pkg.Path, false) != nil {
				continue
			}
			selected[entry.pkg.Path] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

func (s artifactoryService) rpmMemberPackages(
	ctx context.Context, st store.Store, c repo.Config, root string,
	p auth.Principal, acc access,
) ([]rpm.Package, error) {
	manifest, err := s.rpmMemberBytes(ctx, st, c, root+"repodata/repomd.xml", p, acc)
	if err != nil {
		return nil, fmt.Errorf("rpm: parse member repomd: %w", err)
	}
	primaryPath, checksum, err := rpm.DecodeRepomd(manifest, root)
	if err != nil {
		return nil, fmt.Errorf("rpm: parse member repomd: %w", err)
	}
	if !acc.file(c, primaryPath, types.ActionRead) ||
		generic.CheckDownload(c, primaryPath, false) != nil {
		return nil, store.ErrNotFound
	}
	primary, err := s.rpmMemberBytes(ctx, st, c, primaryPath, p, acc)
	if err != nil {
		return nil, err
	}
	packages, err := rpm.DecodePrimary(primary, root, checksum)
	if err != nil {
		return nil, fmt.Errorf("rpm: parse member primary: %w", err)
	}
	return packages, nil
}

func (s artifactoryService) rpmMemberBytes(
	ctx context.Context, st store.Store, c repo.Config, path string,
	p auth.Principal, acc access,
) ([]byte, error) {
	if c.Class == repo.Remote {
		if s.cfg.RemoteSources == nil {
			return nil, source.ErrUnavailable
		}
		q := source.Request{
			Repo: c, Subject: p.Subject(), Access: acc.d, Project: c.ProjectKey,
			Resource: source.Resource{Path: path, Method: http.MethodGet},
		}
		q, err := rpmSourceRequest(q)
		if err != nil {
			return nil, err
		}
		result, err := s.cfg.RemoteSources.Read(ctx, q)
		if err != nil {
			return nil, fmt.Errorf(
				"rpm: remote metadata source: %w",
				pkgroute.SourceError(err, p.Anonymous),
			)
		}
		// The source owns a response body even when a later authority fence
		// fails. Close it before returning from any branch below.
		if result.Stream != nil {
			defer func() { _ = result.Stream.Body.Close() }()
		}
		if result.Revalidate != nil {
			if err := result.Revalidate(ctx); err != nil {
				return nil, fmt.Errorf(
					"rpm: remote metadata revalidate: %w",
					pkgroute.SourceError(err, p.Anonymous),
				)
			}
		}
		if result.CheckRead != nil {
			if err := result.CheckRead(ctx, path); err != nil {
				return nil, fmt.Errorf(
					"rpm: remote metadata read authority: %w",
					pkgroute.SourceError(err, p.Anonymous),
				)
			}
		}
		var body []byte
		switch {
		case result.Stream != nil:
			body, err = boundedRPMMetadata(result.Stream.Body)
		case result.Item != nil:
			body, err = s.rpmBlob(ctx, result.Item.Actual.SHA256)
		default:
			return nil, source.ErrInvalidResult
		}
		if err != nil {
			return nil, err
		}
		if result.Used != nil {
			_ = result.Used(ctx)
		}
		return body, nil
	}
	if c.Class != repo.Local && c.Class != repo.Federated {
		return nil, store.ErrNotFound
	}
	it, err := st.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		return nil, fmt.Errorf("rpm: member metadata item: %w", err)
	}
	if it.Folder {
		return nil, store.ErrNotFound
	}
	return s.rpmBlob(ctx, it.Actual.SHA256)
}

func (s artifactoryService) rpmBlob(ctx context.Context, sha string) ([]byte, error) {
	rc, _, err := s.cfg.Blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("rpm: metadata blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return boundedRPMMetadata(rc)
}

func boundedRPMMetadata(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, rpmMetadataLimit+1))
	if err != nil {
		return nil, fmt.Errorf("rpm: read metadata: %w", err)
	}
	if len(body) > rpmMetadataLimit {
		return nil, errRPMMetadataTooLarge
	}
	return body, nil
}
