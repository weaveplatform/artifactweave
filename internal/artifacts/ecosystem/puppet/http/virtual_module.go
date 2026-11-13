package puppethttp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// virtualModule returns the module record of module name that a virtual
// repository whose members' release pages list no release of it the caller
// may read answers, and false when it has none to answer (#1422) [I]. It
// asks its remote members, in member order, for the record each one's
// remote answers from its upstream's own record of the module
// (unlistedRecord), and answers the first that holds one, as virtualRecord
// answers a release record. JFrog's Puppet repositories page says a
// virtual repository aggregates its local and remote members and that r10k
// is supported, and r10k reads v3/modules/<m> before it reads the release
// record and the archive the virtual repository already answers for such a
// module (#1369), so without this record it cannot install the module.
// RTFACT-15072 and RTFACT-18934 cover a virtual repository's lists and its
// metadata cache only, so what 7.x answers here is [I].
//
// The record is the member's as its remote answers it: its module-wide
// fields are the upstream's, not taken from the releases as a listed
// module's are (derivedModule), since only the current release's own
// record is read. Its links are relative, so they name the virtual
// repository, and the release record and archive they lead to are answered
// through virtualRecord. The virtual repository's Read on the module folder
// is checked first (module), then each member is checked as the merge of
// the release pages checks it (virtual): one the caller may not read the
// module folder of is passed over, and so is one whose read answers 404,
// as a blacked-out member's or one whose patterns reject
// .puppet/<m>/releases.json does (R5). Each release must be readable in
// the virtual repository and in the member. A member whose record lists no
// release the caller may read is passed over too, and any other failure
// fails the answer, as it fails virtualRecord's.
func (h handler) virtualModule(r *http.Request, name string) (moduleRecord, bool, error) {
	for _, key := range pkgroute.RequestFrom(r).Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return moduleRecord{}, false, fmt.Errorf("puppet: virtual member: %w", err)
		}
		if member.Class != repo.Remote {
			continue
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return moduleRecord{}, false, err //nolint:wrapcheck // protocol class refusal
		}
		if !h.k.CanFolder(mr, name, api.ActionRead) {
			continue
		}
		doc, found, err := h.unlistedRecord(mr, name, r)
		if err != nil {
			var response *apierror.Error
			if errors.Is(err, kernel.ErrNotFound) ||
				errors.As(err, &response) && response.Status == http.StatusNotFound {
				continue
			}
			return moduleRecord{}, false, err
		}
		if found {
			return doc, true, nil
		}
	}
	return moduleRecord{}, false, nil
}
