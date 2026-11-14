package containertest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestAccMinIOCreatesABucketAtOnce creates a bucket the moment MinIO is
// handed out, as the acceptance steps and blob contract tests do. Retries
// are off: in #1307 the SDK's three attempts ran out inside MinIO's start-up,
// so a retry here would hide the same fault. The window is milliseconds on
// an idle host, so this proves the start-up path rather than reproducing the
// race; RunMinIO's comment carries why its wait closes the window.
func TestAccMinIOCreatesABucketAtOnce(t *testing.T) {
	ep := MinIO(t)
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(ep.URL),
		Region:       "us-east-1",
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(ep.User, ep.Password, ""),
		Retryer:      aws.NopRetryer{},
	})
	if _, err := client.CreateBucket(
		context.Background(),
		&s3.CreateBucketInput{Bucket: aws.String("at-once")},
	); err != nil {
		t.Fatal(err)
	}
}

// TestAccAzuriteStartsFromItsCopy starts Azurite with its first source on a
// host that cannot resolve, as when MCR does not answer, so only the
// project's copy can serve it (#1463). Until that copy can be pulled
// anonymously, as CI pulls it, the test skips and says what is missing; any
// other answer from GHCR fails it.
func TestAccAzuriteStartsFromItsCopy(t *testing.T) {
	RequireLinux(t)
	ctx := t.Context()
	copyRef := azuriteSources[1]
	refused, err := refusedAnonymously(ctx, copyRef)
	if err != nil {
		t.Fatal(err)
	}
	if refused != "" {
		t.Skipf("GHCR refuses an anonymous pull of Azurite's copy %s (%s): run the "+
			".github/workflows/emulator-copy.yml workflow and make its package public; "+
			"docs/testing/ci.md, \"Azurite's copy\", has the steps", copyRef, refused)
	}
	sources := []string{"mcr.invalid/azure-storage/azurite@" + azuriteDigest, copyRef}
	image, err := requireImage(ctx, sources...)
	if err != nil {
		t.Fatal(err)
	}
	if image != copyRef {
		t.Fatalf("got %s, want the copy %s", image, copyRef)
	}
	_, stop, err := runAzurite(ctx, []string{image})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
}

// refusedAnonymously says how GHCR refuses an anonymous pull of ref, an image
// on ghcr.io named by digest, or "" when it would serve it. It follows the
// distribution token protocol: a token for the repository's pull scope, then
// a HEAD of the manifest by digest. GHCR hands an anonymous token only for a
// public package and answers 403 DENIED for a private or missing one (probed
// 2026-11-14), so 401, 403 and 404 mean the copy is not there for CI yet;
// any other answer is an error.
func refusedAnonymously(ctx context.Context, ref string) (string, error) {
	name, digest, _ := strings.Cut(ref, "@")
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name = name[:colon] // the tag; the pull is by digest
	}
	repo, ok := strings.CutPrefix(name, "ghcr.io/")
	if !ok {
		return "", fmt.Errorf("%s is not on ghcr.io", ref)
	}
	tokenURL := "https://ghcr.io/token?" + url.Values{
		"scope":   {"repository:" + repo + ":pull"},
		"service": {"ghcr.io"},
	}.Encode()
	resp, err := anonymousRequest(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", err
	}
	var token struct {
		Token string `json:"token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&token)
	_ = resp.Body.Close()
	if refused(resp.StatusCode) {
		return "token: " + resp.Status, nil
	}
	if resp.StatusCode != http.StatusOK || err != nil || token.Token == "" {
		return "", fmt.Errorf("GHCR's token endpoint answered %s: %v", resp.Status, err)
	}
	resp, err = anonymousRequest(ctx, http.MethodHead,
		"https://ghcr.io/v2/"+repo+"/manifests/"+digest, map[string]string{
			"Authorization": "Bearer " + token.Token,
			"Accept": strings.Join([]string{
				"application/vnd.oci.image.index.v1+json",
				"application/vnd.docker.distribution.manifest.list.v2+json",
				"application/vnd.oci.image.manifest.v1+json",
				"application/vnd.docker.distribution.manifest.v2+json",
			}, ", "),
		})
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	switch {
	case refused(resp.StatusCode):
		return "manifest: " + resp.Status, nil
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("GHCR answered the manifest HEAD with %s", resp.Status)
	}
	return "", nil
}

func refused(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden ||
		status == http.StatusNotFound
}

func anonymousRequest(
	ctx context.Context, method, target string, header map[string]string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}
