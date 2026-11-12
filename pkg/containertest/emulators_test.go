package containertest

import (
	"context"
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
