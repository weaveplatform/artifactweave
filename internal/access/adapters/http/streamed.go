package accesshttp

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// streamed is the 200 of a complete identity list written from store pages
// (decision 0043, "Streaming complete lists"). It answers every list
// operation that streams, so each handler returns it in place of the
// generated type that would hold the whole list.
type streamed struct{ *httpkit.ListStream }

func (s streamed) VisitGetUsersResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitGetGroupsResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitGetLockedOutUsersResponse(w http.ResponseWriter) error {
	return s.Write(w)
}

func (s streamed) VisitGetPermissionTargetsResponse(w http.ResponseWriter) error {
	return s.Write(w)
}

func (s streamed) VisitGetPermissionTargetsV2Response(w http.ResponseWriter) error {
	return s.Write(w)
}

func (s streamed) VisitGetLegacyTokensResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitGetTokensResponse(w http.ResponseWriter) error { return s.Write(w) }
