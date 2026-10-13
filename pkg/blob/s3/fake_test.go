package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// fakeS3 is an in-memory S3 with just enough fidelity for the store: ranged
// GETs, 404 shapes per operation, multipart uploads with ETag checks, copies
// and paginated listings. failOn makes the Nth and later calls of an
// operation fail, which is how the error paths are reached.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]*fakeMultipart
	nextID  int
	calls   map[string]int
	fail    map[string]failure
	pageLen int32
}

type failure struct {
	after int
	err   error
}

type fakeMultipart struct {
	key   string
	parts map[int32][]byte
}

func newFake() *fakeS3 {
	return &fakeS3{
		objects: map[string][]byte{},
		uploads: map[string]*fakeMultipart{},
		calls:   map[string]int{},
		fail:    map[string]failure{},
		pageLen: 2,
	}
}

// failOn makes op fail with err from its (after+1)th call onwards.
func (f *fakeS3) failOn(op string, after int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[op] = failure{after: after, err: err}
	f.calls[op] = 0
}

func (f *fakeS3) enter(op string) error {
	f.calls[op]++
	if fl, ok := f.fail[op]; ok && f.calls[op] > fl.after {
		return fl.err
	}
	return nil
}

func etag(b []byte) string {
	return fmt.Sprintf("%q", fmt.Sprintf("%x", len(b))+strconv.Itoa(int(sum(b))))
}

func sum(b []byte) byte {
	var s byte
	for _, x := range b {
		s += x
	}
	return s
}

func notFound() error { return &smithy.GenericAPIError{Code: "NotFound", Message: "Not Found"} }

func (f *fakeS3) PutObject(
	_ context.Context,
	in *s3.PutObjectInput,
	_ ...func(*s3.Options),
) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("PutObject"); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.objects[aws.ToString(in.Key)] = b
	return &s3.PutObjectOutput{ETag: aws.String(etag(b))}, nil
}

func (f *fakeS3) GetObject(
	_ context.Context,
	in *s3.GetObjectInput,
	_ ...func(*s3.Options),
) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("GetObject"); err != nil {
		return nil, err
	}
	b, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("no such key")}
	}
	if r := aws.ToString(in.Range); r != "" {
		start, end := parseRange(r, int64(len(b)))
		b = b[start : end+1]
	}
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(b)),
		ContentLength: aws.Int64(int64(len(b))),
	}, nil
}

func parseRange(r string, size int64) (int64, int64) {
	spec := strings.TrimPrefix(r, "bytes=")
	a, b, _ := strings.Cut(spec, "-")
	start, _ := strconv.ParseInt(a, 10, 64)
	end := size - 1
	if b != "" {
		end, _ = strconv.ParseInt(b, 10, 64)
	}
	return start, end
}

func (f *fakeS3) HeadObject(
	_ context.Context,
	in *s3.HeadObjectInput,
	_ ...func(*s3.Options),
) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("HeadObject"); err != nil {
		return nil, err
	}
	b, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, notFound()
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(b)))}, nil
}

func (f *fakeS3) DeleteObject(
	_ context.Context,
	in *s3.DeleteObjectInput,
	_ ...func(*s3.Options),
) (*s3.DeleteObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("DeleteObject"); err != nil {
		return nil, err
	}
	delete(f.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeS3) source(cs string) ([]byte, bool) {
	_, key, _ := strings.Cut(cs, "/")
	b, ok := f.objects[key]
	return b, ok
}

func (f *fakeS3) CopyObject(
	_ context.Context,
	in *s3.CopyObjectInput,
	_ ...func(*s3.Options),
) (*s3.CopyObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CopyObject"); err != nil {
		return nil, err
	}
	b, ok := f.source(aws.ToString(in.CopySource))
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	f.objects[aws.ToString(in.Key)] = append([]byte(nil), b...)
	return &s3.CopyObjectOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(
	_ context.Context,
	in *s3.ListObjectsV2Input,
	_ ...func(*s3.Options),
) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("ListObjectsV2"); err != nil {
		return nil, err
	}
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, aws.ToString(in.Prefix)) && k > aws.ToString(in.ContinuationToken) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := &s3.ListObjectsV2Output{}
	for i, k := range keys {
		if int32(i) == f.pageLen { //nolint:gosec // test sizes are tiny
			out.IsTruncated = aws.Bool(true)
			out.NextContinuationToken = aws.String(keys[i-1])
			break
		}
		out.Contents = append(
			out.Contents,
			types.Object{Key: aws.String(k), Size: aws.Int64(int64(len(f.objects[k])))},
		)
	}
	return out, nil
}

func (f *fakeS3) CreateMultipartUpload(
	_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options),
) (*s3.CreateMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CreateMultipartUpload"); err != nil {
		return nil, err
	}
	f.nextID++
	id := "mp" + strconv.Itoa(f.nextID)
	f.uploads[id] = &fakeMultipart{key: aws.ToString(in.Key), parts: map[int32][]byte{}}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(id)}, nil
}

func (f *fakeS3) upload(id, key string) (*fakeMultipart, error) {
	mp, ok := f.uploads[id]
	if !ok || mp.key != key {
		return nil, &types.NoSuchUpload{}
	}
	return mp, nil
}

func (f *fakeS3) UploadPart(
	_ context.Context,
	in *s3.UploadPartInput,
	_ ...func(*s3.Options),
) (*s3.UploadPartOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UploadPart"); err != nil {
		return nil, err
	}
	mp, err := f.upload(aws.ToString(in.UploadId), aws.ToString(in.Key))
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	mp.parts[aws.ToInt32(in.PartNumber)] = b
	return &s3.UploadPartOutput{ETag: aws.String(etag(b))}, nil
}

func (f *fakeS3) UploadPartCopy(
	_ context.Context, in *s3.UploadPartCopyInput, _ ...func(*s3.Options),
) (*s3.UploadPartCopyOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("UploadPartCopy"); err != nil {
		return nil, err
	}
	mp, err := f.upload(aws.ToString(in.UploadId), aws.ToString(in.Key))
	if err != nil {
		return nil, err
	}
	src, ok := f.source(aws.ToString(in.CopySource))
	if !ok {
		return nil, &types.NoSuchKey{}
	}
	start, end := parseRange(aws.ToString(in.CopySourceRange), int64(len(src)))
	b := append([]byte(nil), src[start:end+1]...)
	mp.parts[aws.ToInt32(in.PartNumber)] = b
	return &s3.UploadPartCopyOutput{
		CopyPartResult: &types.CopyPartResult{ETag: aws.String(etag(b))},
	}, nil
}

func (f *fakeS3) CompleteMultipartUpload(
	_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options),
) (*s3.CompleteMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("CompleteMultipartUpload"); err != nil {
		return nil, err
	}
	mp, err := f.upload(aws.ToString(in.UploadId), aws.ToString(in.Key))
	if err != nil {
		return nil, err
	}
	var all []byte
	for _, p := range in.MultipartUpload.Parts {
		b, ok := mp.parts[aws.ToInt32(p.PartNumber)]
		if !ok || etag(b) != aws.ToString(p.ETag) {
			return nil, &smithy.GenericAPIError{Code: "InvalidPart"}
		}
		all = append(all, b...)
	}
	f.objects[mp.key] = all
	delete(f.uploads, aws.ToString(in.UploadId))
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (f *fakeS3) AbortMultipartUpload(
	_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options),
) (*s3.AbortMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.enter("AbortMultipartUpload"); err != nil {
		return nil, err
	}
	if _, err := f.upload(aws.ToString(in.UploadId), aws.ToString(in.Key)); err != nil {
		return nil, err
	}
	delete(f.uploads, aws.ToString(in.UploadId))
	return &s3.AbortMultipartUploadOutput{}, nil
}

type fakePresign struct{ err error }

func (p fakePresign) PresignGetObject(
	_ context.Context, in *s3.GetObjectInput, opts ...func(*s3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) {
	if p.err != nil {
		return nil, p.err
	}
	var o s3.PresignOptions
	for _, fn := range opts {
		fn(&o)
	}
	return &v4.PresignedHTTPRequest{
		URL:    "https://fake.invalid/" + aws.ToString(in.Key) + "?expires=" + o.Expires.String(),
		Method: "GET",
	}, nil
}
