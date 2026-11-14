package artifactshttp

import (
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// streamed is the 200 of a complete list written from store pages
// (decision 0043, "Streaming complete lists"). It answers every list
// operation here that streams, so each handler returns it in place of the
// generated type that would hold the whole list.
type streamed struct{ *httpkit.ListStream }

func (s streamed) VisitGetAllBuildsResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitGetBuildRunsResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitSearchArtifactResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitSearchBadChecksumResponse(w http.ResponseWriter) error {
	return s.Write(w)
}

func (s streamed) VisitSearchChecksumResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitSearchCreationResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitSearchDatesResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitSearchPatternResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitSearchPropertyResponse(w http.ResponseWriter) error { return s.Write(w) }

func (s streamed) VisitSearchUsageResponse(w http.ResponseWriter) error { return s.Write(w) }
