package artifactory_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	artifactory "github.com/weaveplatform/artifactweave/pkg/client/artifactory"
)

// Every operation that admission charges declares its 429, so the client
// decodes the refusal and its rate headers: wait at least Retry-After before
// the next call, and read RateLimit-Policy to learn which allowance ran out.
// The test server stands in for artifactweave refusing a caller whose
// allowance of one request a minute is spent.
func ExampleClientWithResponses_GetSystemVersionWithResponse_rateLimited() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "45")
		w.Header().Set("RateLimit-Policy", `"api";q=1;w=60`)
		w.Header().Set("RateLimit", `"api";r=0;t=45`)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"errors":[{"status":429,"message":"API request limit exceeded"}]}`)
	}))
	defer server.Close()
	client, err := artifactory.NewClientWithResponses(server.URL + "/artifactory")
	if err != nil {
		return
	}
	response, err := client.GetSystemVersionWithResponse(context.Background(), nil)
	if err != nil {
		return
	}
	if limited := response.Headers429; limited != nil && response.JSON429 != nil {
		fmt.Println(response.StatusCode(), response.JSON429.Errors[0].Message)
		fmt.Println("retry after", limited.RetryAfter, "seconds")
		fmt.Println("policy", *limited.RateLimitPolicy)
		fmt.Println("remaining", *limited.RateLimit)
	}
	// Output:
	// 429 API request limit exceeded
	// retry after 45 seconds
	// policy "api";q=1;w=60
	// remaining "api";r=0;t=45
}
