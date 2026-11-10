package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The API restriction routes, as access/adapters/http serves them.
const (
	apiRestrictionPath      = "/artifactory/api/artifactweave/api-restrictions/"
	apiRestrictionAuditPath = "/artifactory/api/artifactweave/api-restriction-audit"
)

func restrictionBody() string {
	return fmt.Sprintf(
		`{"until":%q,"reason":"Stop expensive polling"}`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	)
}

var apiRestrictionStatusPart = statusPart{
	generated: map[string]bool{
		"GET " + apiRestrictionPath + "{userName}":    true,
		"PUT " + apiRestrictionPath + "{userName}":    true,
		"DELETE " + apiRestrictionPath + "{userName}": true,
		"GET " + apiRestrictionAuditPath:              true,
	},
	rows: func() []statusCase {
		var rows []statusCase
		for _, method := range []string{"GET", "PUT", "DELETE"} {
			body, status := "", 200
			if method == "PUT" {
				body = restrictionBody()
			}
			if method == "DELETE" {
				status = 204
			}
			route := method + " " + apiRestrictionPath + "{userName}"
			rows = append(
				rows,
				statusCase{route, apiRestrictionPath + "user", "admin", body, status},
				statusCase{route, apiRestrictionPath + "user", "user", body, 403},
				statusCase{route, apiRestrictionPath + "user", "", body, 401},
			)
		}
		return append(
			rows,
			statusCase{"GET " + apiRestrictionAuditPath, apiRestrictionAuditPath, "admin", "", 200},
			statusCase{"GET " + apiRestrictionAuditPath, apiRestrictionAuditPath, "user", "", 403},
			statusCase{"GET " + apiRestrictionAuditPath, apiRestrictionAuditPath, "", "", 401},
		)
	},
}

var apiRestrictionMatrixPart = matrixPart{
	seed: func(t *testing.T, f *matrixFixture) {
		if err := f.db.Users().
			Create(t.Context(), types.User{Name: "restriction-target"}); err != nil {
			t.Fatal(err)
		}
	},
	rows: func() []matrixRow {
		return []matrixRow{
			row(
				"GET "+apiRestrictionPath+"{userName}",
				apiRestrictionPath+"restriction-target",
				"",
				administrators,
				200,
			),
			row(
				"PUT "+apiRestrictionPath+"{userName}",
				apiRestrictionPath+"restriction-target",
				restrictionBody(),
				administrators,
				200,
			),
			row(
				"DELETE "+apiRestrictionPath+"{userName}",
				apiRestrictionPath+"restriction-target",
				"",
				administrators,
				204,
			),
			row("GET "+apiRestrictionAuditPath, apiRestrictionAuditPath, "", administrators, 200),
		}
	},
}
