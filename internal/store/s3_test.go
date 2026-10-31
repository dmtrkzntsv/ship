package store_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/dmtrkzntsv/ship/internal/store"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBucket = "lab"

// putHeaders records the Cache-Control header of each PUT, by request path.
type putHeaders struct {
	mu sync.Mutex
	cc map[string]string
}

func newTestStore(t *testing.T) (store.Store, *s3.Client, *putHeaders) {
	t.Helper()
	rec := &putHeaders{cc: map[string]string{}}
	fake := gofakes3.New(s3mem.New()).Server()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			rec.mu.Lock()
			rec.cc[r.URL.Path] = r.Header.Get("Cache-Control")
			rec.mu.Unlock()
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider("AK", "SK", ""),
	})
	_, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(testBucket)})
	require.NoError(t, err)

	st, err := store.NewS3(store.S3Options{Endpoint: srv.URL, AccessKeyID: "AK", SecretAccessKey: "SK", Bucket: testBucket})
	require.NoError(t, err)
	return st, client, rec
}

func putStr(t *testing.T, st store.Store, key, data string) {
	t.Helper()
	require.NoError(t, st.Put(context.Background(), key, bytes.NewReader([]byte(data)), int64(len(data)),
		store.PutOptions{CacheControl: "no-cache", ContentType: "application/octet-stream"}))
}

func TestS3PutGetListDirsDelete(t *testing.T) {
	ctx := context.Background()
	st, client, rec := newTestStore(t)

	putStr(t, st, "econumo/v1/a", "hello")
	putStr(t, st, "econumo/latest/a", "hi")
	putStr(t, st, "billing/v1/a", "x")

	rc, err := st.Get(ctx, "econumo/v1/a")
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, "hello", string(b))

	objs, err := st.List(ctx, "econumo/")
	require.NoError(t, err)
	require.Len(t, objs, 2)
	assert.Equal(t, "econumo/latest/a", objs[0].Key)
	assert.Equal(t, int64(2), objs[0].Size)
	assert.Equal(t, "econumo/v1/a", objs[1].Key)
	assert.False(t, objs[1].LastModified.IsZero())

	dirs, err := st.ListDirs(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"billing", "econumo"}, dirs)
	dirs, err = st.ListDirs(ctx, "econumo/")
	require.NoError(t, err)
	assert.Equal(t, []string{"latest", "v1"}, dirs)

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(testBucket), Key: aws.String("econumo/v1/a")})
	require.NoError(t, err)
	assert.Equal(t, "application/octet-stream", aws.ToString(head.ContentType))
	// gofakes3 does not persist Cache-Control (HeadObject returns it empty), so assert
	// that the store sent it on the wire instead.
	rec.mu.Lock()
	assert.Equal(t, "no-cache", rec.cc["/"+testBucket+"/econumo/v1/a"])
	rec.mu.Unlock()

	require.NoError(t, st.Delete(ctx, []string{"econumo/v1/a", "econumo/latest/a"}))
	objs, err = st.List(ctx, "econumo/")
	require.NoError(t, err)
	assert.Empty(t, objs)
}

func TestS3GetMissing(t *testing.T) {
	st, _, _ := newTestStore(t)
	_, err := st.Get(context.Background(), "nope")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestS3ListPagination(t *testing.T) {
	ctx := context.Background()
	st, _, _ := newTestStore(t)
	for i := 0; i < 1105; i++ {
		putStr(t, st, fmt.Sprintf("p/d%04d/f", i), "x")
	}
	objs, err := st.List(ctx, "p/")
	require.NoError(t, err)
	assert.Len(t, objs, 1105)
	dirs, err := st.ListDirs(ctx, "p/")
	require.NoError(t, err)
	assert.Len(t, dirs, 1105)

	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	require.NoError(t, st.Delete(ctx, keys)) // > 1000 keys: batched
	objs, err = st.List(ctx, "p/")
	require.NoError(t, err)
	assert.Empty(t, objs)
}

func TestNewS3RequiresCredentials(t *testing.T) {
	_, err := store.NewS3(store.S3Options{Endpoint: "http://x", Bucket: "b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "R2 credentials are required")
}
