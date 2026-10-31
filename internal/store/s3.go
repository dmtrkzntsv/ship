package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// S3Options configures NewS3. All fields are required.
type S3Options struct{ Endpoint, AccessKeyID, SecretAccessKey, Bucket string }

type s3Store struct {
	c      *s3.Client
	bucket string
}

const deleteBatch = 1000

// NewS3 returns a Store backed by an S3-compatible bucket (Cloudflare R2).
func NewS3(o S3Options) (Store, error) {
	if o.AccessKeyID == "" || o.SecretAccessKey == "" {
		return nil, errors.New("R2 credentials are required")
	}
	if o.Endpoint == "" {
		return nil, errors.New("R2 endpoint is required")
	}
	if o.Bucket == "" {
		return nil, errors.New("R2 bucket is required")
	}
	c := s3.New(s3.Options{
		Region:                     "auto",
		BaseEndpoint:               aws.String(o.Endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(o.AccessKeyID, o.SecretAccessKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &s3Store{c: c, bucket: o.Bucket}, nil
}

// mapErr wraps err with ErrNotFound / ErrAccessDenied where applicable.
func mapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	if errors.As(err, &nsk) || errors.As(err, &nf) {
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	}
	var re *smithyhttp.ResponseError
	status := 0
	if errors.As(err, &re) {
		status = re.HTTPStatusCode()
	}
	var api smithy.APIError
	code := ""
	if errors.As(err, &api) {
		code = api.ErrorCode()
	}
	switch {
	case code == "NoSuchKey" || code == "NotFound" || status == http.StatusNotFound:
		return fmt.Errorf("%s: %w", op, ErrNotFound)
	case code == "AccessDenied" || status == http.StatusForbidden:
		return fmt.Errorf("%s: %w", op, ErrAccessDenied)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	p := s3.NewListObjectsV2Paginator(s.c, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, mapErr("list "+prefix, err)
		}
		for _, o := range page.Contents {
			obj := Object{Key: aws.ToString(o.Key), Size: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				obj.LastModified = *o.LastModified
			}
			out = append(out, obj)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *s3Store) ListDirs(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	p := s3.NewListObjectsV2Paginator(s.c, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(prefix), Delimiter: aws.String("/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, mapErr("list dirs "+prefix, err)
		}
		for _, cp := range page.CommonPrefixes {
			name := aws.ToString(cp.Prefix)[len(prefix):]
			if name = trimSlash(name); name != "" {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func trimSlash(s string) string {
	if n := len(s); n > 0 && s[n-1] == '/' {
		return s[:n-1]
	}
	return s
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, mapErr("get "+key, err)
	}
	return r.Body, nil
}

func (s *s3Store) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, o PutOptions) error {
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: body, ContentLength: aws.Int64(size)}
	if o.CacheControl != "" {
		in.CacheControl = aws.String(o.CacheControl)
	}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	_, err := s.c.PutObject(ctx, in)
	return mapErr("put "+key, err)
}

func (s *s3Store) Delete(ctx context.Context, keys []string) error {
	for start := 0; start < len(keys); start += deleteBatch {
		end := min(start+deleteBatch, len(keys))
		ids := make([]types.ObjectIdentifier, 0, end-start)
		for _, k := range keys[start:end] {
			ids = append(ids, types.ObjectIdentifier{Key: aws.String(k)})
		}
		out, err := s.c.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &s.bucket,
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return mapErr("delete", err)
		}
		if len(out.Errors) > 0 {
			e := out.Errors[0]
			err := fmt.Errorf("delete %s: %s: %s", aws.ToString(e.Key), aws.ToString(e.Code), aws.ToString(e.Message))
			if aws.ToString(e.Code) == "AccessDenied" {
				err = fmt.Errorf("delete %s: %w", aws.ToString(e.Key), ErrAccessDenied)
			}
			return err
		}
	}
	return nil
}
