package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Discover requires the caller's existing permit. Public adapters invoke it
// through RequireSmart and source.Service, never manufacture their own permit.
func (s *SmartRemote) Discover(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
) (SmartPeer, error) {
	peer, err := ParseSmartPeer(q.Repo)
	if err != nil || permit.Intent != source.MayFetch || !permit.UpstreamNetworkAllowed ||
		enabled(q.Repo.Offline) {
		return SmartPeer{}, source.ErrDenied
	}
	opts, err := s.policy.Options(ctx, q.Repo)
	if err != nil {
		return SmartPeer{}, policyError(err)
	}
	if err = s.policy.check(ctx, q, permit, opts.Revision); err != nil {
		return SmartPeer{}, err
	}
	peer.revision = opts.Revision
	state, err := s.policy.state(ctx, q, opts.Revision)
	if err != nil {
		return SmartPeer{}, err
	}
	if cached := state.Smart; cached != nil && cached.BaseURL == peer.BaseURL &&
		cached.Repository == peer.Repository && s.policy.now().Before(cached.CheckedAt.Add(smartTTL(q.Repo))) {
		peer.Class, peer.Version = repo.Class(cached.Class), cached.Version
		return peer, smartCapabilities(q.Repo, peer)
	}
	if err = s.policy.cooldown(state); err != nil {
		return SmartPeer{}, err
	}
	key := s.policy.flightKey(
		types.RemoteCacheKey{
			RepoKey:          q.Repo.Key,
			SettingsRevision: opts.Revision,
			Variant:          "smart-discovery-v1",
		},
		permit.Identity,
	)
	peer, err = s.discovery.do(ctx, key, func(shared context.Context) (SmartPeer, error) {
		return s.discover(shared, q, permit, opts, peer)
	})
	if err == nil {
		err = s.policy.check(ctx, q, permit, opts.Revision)
	}
	return peer, smartError("discovery", err)
}

func smartTTL(c repo.Config) time.Duration {
	if c.RetrievalCachePeriodSecs != nil {
		return time.Duration(*c.RetrievalCachePeriodSecs) * time.Second
	}
	return 2 * time.Hour
}

func (s *SmartRemote) discover(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	peer SmartPeer,
) (SmartPeer, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	release, err := s.policy.capacity.acquire(ctx, q.Repo.Key)
	if err != nil {
		return SmartPeer{}, policyError(err)
	}
	defer release()
	version, noStore, err := s.document(ctx, q, permit, opts, peer.BaseURL+"/api/system/version")
	if err != nil {
		return SmartPeer{}, err
	}
	_ = json.Unmarshal(version["version"], &peer.Version)
	endpoint := peer.BaseURL + "/api/repositories/" + url.PathEscape(peer.Repository)
	config, private, err := s.document(ctx, q, permit, opts, endpoint)
	var failure *source.HTTPError
	if errors.As(err, &failure) && failure.Status == http.StatusForbidden {
		config, private, err = s.document(ctx, q, permit, opts, endpoint+"/configuration")
	}
	if err != nil {
		return SmartPeer{}, err
	}
	_ = json.Unmarshal(config["rclass"], &peer.Class)
	if peer.Class == "" {
		_ = json.Unmarshal(config["type"], &peer.Class)
	}
	if err = smartCapabilities(q.Repo, peer); err != nil {
		return SmartPeer{}, err
	}
	if !noStore && !private &&
		(q.Repo.StoreArtifactsLocally == nil || *q.Repo.StoreArtifactsLocally) {
		err = s.policy.Store.Tx(ctx, func(tx context.Context) error {
			if e := s.policy.check(tx, q, permit, opts.Revision); e != nil {
				return smartError("operation", e)
			}
			state, e := s.policy.state(tx, q, opts.Revision)
			if e != nil {
				return smartError("operation", e)
			}
			state.Smart = &types.RemoteSmartPeer{
				BaseURL: peer.BaseURL, Repository: peer.Repository,
				Class: string(peer.Class), Version: peer.Version, CheckedAt: s.policy.now(),
			}
			return s.policy.Store.RemoteCache().PutState(tx, q.Repo.Key, state)
		})
	}
	return peer, smartError("discovery", err)
}

func smartCapabilities(c repo.Config, peer SmartPeer) error {
	parts := strings.Split(peer.Version, ".")
	if len(parts) != 3 {
		return source.ErrUnsupported
	}
	version := [3]int{}
	for i, raw := range parts {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100000 {
			return source.ErrUnsupported
		}
		version[i] = n
	}
	if version[0] < 4 || version[0] == 4 && version[1] < 1 {
		return source.ErrUnsupported
	}
	if peer.Class != repo.Local && peer.Class != repo.Remote && peer.Class != repo.Federated &&
		peer.Class != repo.Virtual {
		return source.ErrUnsupported
	}
	if c.PackageType == repo.Go {
		// Go needs a virtual producer. P10 owns local virtual execution; this
		// adapter can already negotiate an external peer which supplies one.
		if peer.Class != repo.Virtual {
			return source.ErrUnsupported
		}
	} else if peer.Class == repo.Virtual {
		return source.ErrUnsupported
	}
	if smartStatistics(c) && version[0] == 4 && version[1] < 2 ||
		smartAbsence(c) && version[0] == 4 &&
			(version[1] < 3 || version[1] == 3 && version[2] < 3) {
		return source.ErrUnsupported
	}
	return nil
}

func (s *SmartRemote) document(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
	target string,
) (map[string]json.RawMessage, bool, error) {
	res, err := s.request(ctx, q, permit, opts, http.MethodGet, target, nil)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusTooManyRequests {
			return nil, false, responseError(res, s.policy.now())
		}
		return nil, false, smartStatus(res.StatusCode)
	}
	typ, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if typ != "application/json" &&
		(!strings.HasPrefix(typ, "application/") || !strings.HasSuffix(typ, "+json")) {
		return nil, false, source.ErrInvalidResult
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	var doc map[string]json.RawMessage
	if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &doc) != nil || doc == nil {
		return nil, false, source.ErrInvalidResult
	}
	if err = s.policy.check(ctx, q, permit, opts.Revision); err != nil {
		return nil, false, err
	}
	return doc, parseCacheRules(res.Header.Get("Cache-Control")).noStore ||
		!cacheableVary(res.Header.Get("Vary")), nil
}

func smartStatus(status int) error {
	if status == http.StatusNotFound {
		return store.ErrNotFound
	}
	if status >= 400 && status <= 599 {
		return &source.HTTPError{Status: status, Kind: source.HTTPFailure}
	}
	return fmt.Errorf("smart remote protocol response: %w", source.ErrInvalidResult)
}
