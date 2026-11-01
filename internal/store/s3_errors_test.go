package store_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dmtrkzntsv/ship/internal/store"
)

// s3Reply is a canned S3 answer: an HTTP status and an XML body.
type s3Reply struct {
	status int
	body   string
}

func s3Error(status int, code string) s3Reply {
	return s3Reply{status, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + code + `</Code><Message>m</Message></Error>`}
}

// scriptedS3 serves the four S3 calls the store makes (ListObjectsV2,
// GetObject, PutObject, DeleteObjects) with fixed replies, recording the
// call names it saw.
func scriptedS3(t *testing.T, replies map[string]s3Reply) (store.Store, func() []string) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var op string
		switch {
		case r.Method == http.MethodGet && q.Get("list-type") == "2":
			op = "list"
		case r.Method == http.MethodGet:
			op = "get"
		case r.Method == http.MethodPut:
			op = "put"
		case r.Method == http.MethodPost && q.Has("delete"):
			op = "delete"
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		calls = append(calls, op)
		mu.Unlock()
		rep, ok := replies[op]
		if !ok {
			t.Errorf("no reply scripted for %s", op)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(rep.status)
		_, _ = w.Write([]byte(rep.body))
	}))
	t.Cleanup(srv.Close)
	st, err := store.NewS3(store.S3Options{Endpoint: srv.URL, AccessKeyID: "AK", SecretAccessKey: "SK", Bucket: testBucket})
	require.NoError(t, err)
	return st, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// callAll runs each store operation once and returns their errors by name.
func callAll(st store.Store) map[string]error {
	ctx := context.Background()
	_, listErr := st.List(ctx, "p/")
	_, dirsErr := st.ListDirs(ctx, "p/")
	_, getErr := st.Get(ctx, "p/k")
	putErr := st.Put(ctx, "p/k", bytes.NewReader([]byte("x")), 1, store.PutOptions{})
	delErr := st.Delete(ctx, []string{"p/k"})
	return map[string]error{"list": listErr, "list dirs": dirsErr, "get": getErr, "put": putErr, "delete": delErr}
}

func allReply(r s3Reply) map[string]s3Reply {
	return map[string]s3Reply{"list": r, "get": r, "put": r, "delete": r}
}

func TestS3ErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		reply s3Reply
		want  error
	}{
		"404 with code":        {s3Error(http.StatusNotFound, "NoSuchBucket"), store.ErrNotFound},
		"NoSuchKey code":       {s3Error(http.StatusBadRequest, "NoSuchKey"), store.ErrNotFound},
		"403 access denied":    {s3Error(http.StatusForbidden, "AccessDenied"), store.ErrAccessDenied},
		"403 other code":       {s3Error(http.StatusForbidden, "SignatureDoesNotMatch"), store.ErrAccessDenied},
		"AccessDenied any 4xx": {s3Error(http.StatusBadRequest, "AccessDenied"), store.ErrAccessDenied},
	} {
		t.Run(name, func(t *testing.T) {
			st, _ := scriptedS3(t, allReply(tc.reply))
			for op, err := range callAll(st) {
				assert.ErrorIs(t, err, tc.want, op)
			}
		})
	}
}

func TestS3OtherErrorsPassThrough(t *testing.T) {
	st, _ := scriptedS3(t, allReply(s3Error(http.StatusBadRequest, "InvalidArgument")))
	errs := callAll(st)
	prefixes := map[string]string{"list": "list p/: ", "list dirs": "list dirs p/: ", "get": "get p/k: ", "put": "put p/k: ", "delete": "delete: "}
	for op, err := range errs {
		require.Error(t, err, op)
		assert.NotErrorIs(t, err, store.ErrNotFound, op)
		assert.NotErrorIs(t, err, store.ErrAccessDenied, op)
		assert.Contains(t, err.Error(), "InvalidArgument", op)
		assert.Regexp(t, "^"+prefixes[op], err.Error(), op)
	}
}

func TestS3DeleteReportsPerKeyErrors(t *testing.T) {
	deleteResult := func(code string) s3Reply {
		return s3Reply{http.StatusOK, `<?xml version="1.0" encoding="UTF-8"?><DeleteResult>` +
			`<Error><Key>p/k</Key><Code>` + code + `</Code><Message>nope</Message></Error></DeleteResult>`}
	}

	st, _ := scriptedS3(t, map[string]s3Reply{"delete": deleteResult("AccessDenied")})
	err := st.Delete(context.Background(), []string{"p/k"})
	require.ErrorIs(t, err, store.ErrAccessDenied)
	assert.Equal(t, "delete p/k: access denied", err.Error())

	st, _ = scriptedS3(t, map[string]s3Reply{"delete": deleteResult("InternalError")})
	err = st.Delete(context.Background(), []string{"p/k"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, store.ErrAccessDenied)
	assert.Equal(t, "delete p/k: InternalError: nope", err.Error())
}

func TestS3DeleteNothingMakesNoCall(t *testing.T) {
	st, calls := scriptedS3(t, nil)
	require.NoError(t, st.Delete(context.Background(), nil))
	assert.Empty(t, calls())
}

func TestS3ListOddEntries(t *testing.T) {
	// A prefix without a trailing slash is kept as is, the prefix itself is
	// skipped, and an object without LastModified gets the zero time.
	const listing = `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><Name>lab</Name><Prefix>p/</Prefix>` +
		`<KeyCount>3</KeyCount><IsTruncated>false</IsTruncated>` +
		`<Contents><Key>p/k</Key><Size>3</Size></Contents>` +
		`<CommonPrefixes><Prefix>p/x</Prefix></CommonPrefixes>` +
		`<CommonPrefixes><Prefix>p/</Prefix></CommonPrefixes>` +
		`<CommonPrefixes><Prefix>p/a/</Prefix></CommonPrefixes>` +
		`</ListBucketResult>`
	st, _ := scriptedS3(t, map[string]s3Reply{"list": {http.StatusOK, listing}})

	dirs, err := st.ListDirs(context.Background(), "p/")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "x"}, dirs)

	objs, err := st.List(context.Background(), "p/")
	require.NoError(t, err)
	assert.Equal(t, []store.Object{{Key: "p/k", Size: 3}}, objs)
}

func TestNewS3RequiresEndpointAndBucket(t *testing.T) {
	_, err := store.NewS3(store.S3Options{AccessKeyID: "AK", SecretAccessKey: "SK", Bucket: "b"})
	require.EqualError(t, err, "R2 endpoint is required")
	_, err = store.NewS3(store.S3Options{Endpoint: "http://x", AccessKeyID: "AK", SecretAccessKey: "SK"})
	require.EqualError(t, err, "R2 bucket is required")
}
