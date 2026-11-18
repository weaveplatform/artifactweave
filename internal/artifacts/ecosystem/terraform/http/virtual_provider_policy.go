package terraformhttp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (h handler) virtualProviderVersionFence(r *http.Request, v virtualProviderVersion) error {
	for _, read := range v.reads {
		if read.Revalidate != nil {
			if err := read.Revalidate(r.Context()); err != nil {
				return fmt.Errorf("terraform provider source fence: %w", err)
			}
		}
	}
	mr, err := h.currentProviderMember(r, v.member)
	if err != nil {
		return err
	}

	for _, path := range v.paths {
		allowed, e := h.virtualProviderAllowed(r, mr, path)
		if e != nil {
			return e
		}
		if !allowed {
			return missing()
		}
		if len(v.reads) > 0 && v.reads[0].CheckPermission != nil {
			if e := v.reads[0].CheckPermission(r.Context(), path); e != nil {
				return fmt.Errorf("terraform provider listing permission: %w", e)
			}
		}
	}
	files := v.files
	if v.local != nil {
		files = v.paths
	}
	for _, path := range files {
		allowed, err := h.virtualProviderAllowed(r, mr, path)
		if err != nil {
			return err
		}
		if !allowed {
			return missing()
		}
	}

	return nil
}

func (h handler) virtualProviderFileFence(r, mr *http.Request, paths []string) error {
	current, err := h.currentProviderMember(r, mr)
	if err != nil {
		return err
	}
	for _, path := range paths {
		allowed, err := h.virtualProviderAllowed(r, current, path)
		if err != nil {
			return err
		}
		if !allowed {
			return missing()
		}
	}
	return nil
}

func (h handler) virtualProviderAdmit(r, mr *http.Request, paths []string) error {
	for _, path := range paths {
		allowed, err := h.virtualProviderAllowed(r, mr, path)
		if err != nil {
			return err
		}
		if !allowed {
			return missing()
		}
	}
	return nil
}

// Reauthorize the signer while retaining the real member's route/source and
// cache-only binding. A public virtual context cannot replace that binding.
func (h handler) currentProviderMember(r, member *http.Request) (*http.Request, error) {
	original, _ := kernel.FromContext(r.Context())
	var current kernel.Principal
	var d api.Decider
	var err error
	if h.o.Authenticator != nil {
		current, err = h.o.Authenticator.Current(r.Context(), original)
		if err == nil {
			current, d, err = h.o.Authorizer.Authorize(r.Context(), current, current.Anonymous)
		}
	} else {
		// A protocol mounted without authentication may supply a trusted
		// principal, but cannot refresh a verified password/token reference.
		if reference, e := original.DownloadReference(); e == nil && reference.Kind != "anonymous" {
			return nil, missing()
		}
		current, d, err = h.principal(r, original.Name)
	}
	if err != nil {
		if signedAuthorityRefusal(err) {
			return nil, missing()
		}
		return nil, err
	}
	before := pkgroute.RequestFrom(member).Repo
	after, err := h.k.DB().Repos().Get(r.Context(), before.Key)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, missing()
	}
	if err != nil {
		return nil, fmt.Errorf("terraform current provider member: %w", err)
	}
	if before.Class != after.Class || !providerMember(after) {
		return nil, missing()
	}
	return member.WithContext(api.WithCaller(member.Context(), current, d)), nil
}
