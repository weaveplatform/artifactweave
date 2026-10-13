package s3

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ClientConfig mirrors the blob.s3 section of the application configuration.
type ClientConfig struct {
	// Endpoint overrides the AWS endpoint, for MinIO and other S3-compatible
	// stores, e.g. "http://minio:9000". Empty means AWS.
	Endpoint string
	// Region is required by the signer even for stores that ignore it.
	Region string
	// PathStyle addresses buckets as <endpoint>/<bucket> instead of
	// <bucket>.<endpoint>; MinIO and most self-hosted stores need it.
	PathStyle bool
	// AccessKey and SecretKey are static credentials. Both empty means the
	// AWS default chain (environment, shared config, instance role).
	AccessKey string
	SecretKey string
	// Profile selects a named profile of the shared AWS configuration, so
	// stores in one process can use different accounts. Empty means the
	// default profile (or AWS_PROFILE).
	Profile string
}

// NewClient builds the S3 client and presigner for cfg.
func NewClient(ctx context.Context, cfg ClientConfig) (*s3.Client, *s3.PresignClient, error) {
	opts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	if cfg.Profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(cfg.Profile))
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("blob/s3: load AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.PathStyle
	})
	return client, s3.NewPresignClient(client), nil
}
