package api

import "net/http"

// The API key headers 7.x's props filter reads: X-JFrog-Art-Api, and
// X-Api-Key, its older name (ApiKeyManager.API_KEY_HEADER and
// OLD_API_KEY_HEADER, 7.161.26).
const (
	apiKeyHeader    = "X-JFrog-Art-Api" //nolint:gosec // a header's name, not a credential
	oldAPIKeyHeader = "X-Api-Key"       //nolint:gosec // a header's name, not a credential
)

// APIKey returns the API key a request sends as 7.x reads it, reporting
// whether it sends one: X-JFrog-Art-Api unless it is blank, and otherwise
// X-Api-Key whenever it is sent, even empty
// (AuthenticationFilterUtils.getApiKeyTokenKeyValue, 7.161.26). The props
// filter that reads it comes before the Basic filter in the chain
// (AuthenticationFilterListSorter) and accepts every request that sends a
// key (PropsAuthenticationFilter.acceptFilter), so such a request is
// authenticated by the key alone, whatever its Authorization header says.
// The key comes back as it was sent, first header line only, as
// HttpServletRequest.getHeader reads it.
func APIKey(h http.Header) (string, bool) {
	if v := h.Get(apiKeyHeader); !Blank(v) {
		return v, true
	}
	if v := h.Values(oldAPIKeyHeader); len(v) > 0 {
		return v[0], true
	}
	return "", false
}

// APIKeySent reports whether either API key header is not blank, which is
// when 7.x's entry point answers a refused credential 403 rather than 401,
// "if not the client keeps asking for credentials indefinitely on 401
// responses" (ArtifactoryBasicAuthenticationEntryPoint.isAlreadyAuthedApiKeyRequest,
// 7.161.26). An X-Api-Key sent blank is read as a key and refused, but is
// answered 401.
func APIKeySent(h http.Header) bool {
	return !Blank(h.Get(apiKeyHeader)) || !Blank(h.Get(oldAPIKeyHeader))
}
