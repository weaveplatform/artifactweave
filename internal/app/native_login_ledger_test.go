package app

import "strings"

func apiReviewNativeLogin(e *apiReviewOperation) {
	_, path, _ := strings.Cut(e.Route, " ")
	if path != "/ui/native-login" && path != "/ui/terraform/oauth2/authorize" && path != "/artifactory/api/oauth2/token" && path != "/artifactory/api/npm/{repoKey}/-/v1/login" &&
		path != "/artifactory/api/npm/{repoKey}/-/v1/done" {
		return
	}
	e.Family = "native-client-approval"
	e.State = "implemented when native_login.enabled; absent by default"
	e.Admission = "explicit trusted canonical origin; bounded process-local capabilities; browser session and CSRF plus exact Origin for form mutations; current internal user/UI eligibility and npm repository incarnation/class/owner/visibility rechecked under shared administration guard"
	e.Pagination = "not a collection; at most1024 outstanding approvals, lazy expiry, no transaction across password authentication/directory I/O"
	e.Effects = "browser/session approval state only; no credential issued before authenticated consent; restart cancels outstanding state"
	e.Retry = "no Idempotency-Key; reconcile approval status; a new start allocates a distinct approval"
	if path == "/artifactory/api/npm/{repoKey}/-/v1/done" || path == "/artifactory/api/oauth2/token" {
		e.Effects = "single-winner capability consumption and guarded P4 revocable finite token issuance; no refresh token or token replay storage; HEAD never redeems"
		e.Retry = "pending npm202 honors Retry-After; do not repeat a delivered, denied, expired or consumed capability; issuance failure or response loss requires a new login, never a second mint"
	}
	e.Evidence = []string{
		"internal/access/core/session/service.go",
		"internal/access/adapters/http/login/http.go",
		"internal/access/module.go",
		"internal/app/status_native_login_test.go",
		"test/server/access/native_login_test.go",
		"test/acceptance/features/native_login.feature",
		"docs/research/decisions/0019-human-authentication-tiers.md",
	}
}
