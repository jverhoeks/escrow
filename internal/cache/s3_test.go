package cache_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jverhoeks/escrow/internal/cache"
)

// mockS3 is an in-memory S3 API server for testing. It implements the subset
// of S3 operations that S3Cache uses: HeadBucket, GetObject, PutObject,
// HeadObject, ListObjectsV2, DeleteObjects.
type mockS3 struct {
	mu      sync.Mutex
	objects map[string][]byte // key → content
}

func newMockS3() *mockS3 {
	return &mockS3{objects: map[string][]byte{}}
}

// s3ErrorResponse is a minimal S3 error XML body.
type s3ErrorResponse struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func writeS3Error(w http.ResponseWriter, code, msg string, status int) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	xml.NewEncoder(w).Encode(s3ErrorResponse{Code: code, Message: msg})
}

// s3ListResult is the ListObjectsV2 response.
type s3ListResult struct {
	XMLName     xml.Name        `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name        string          `xml:"Name"`
	Prefix      string          `xml:"Prefix"`
	KeyCount    int             `xml:"KeyCount"`
	IsTruncated bool            `xml:"IsTruncated"`
	Contents    []s3ListEntry   `xml:"Contents"`
}

type s3ListEntry struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
}

// s3DeleteResult is the DeleteObjects response.
type s3DeletedEntry struct {
	Key string `xml:"Key"`
}

type s3DeleteResult struct {
	XMLName xml.Name         `xml:"http://s3.amazonaws.com/doc/2006-03-01/ DeleteResult"`
	Deleted []s3DeletedEntry `xml:"Deleted"`
}

// s3DeleteRequest is the DeleteObjects request body.
type s3DeleteRequest struct {
	XMLName xml.Name `xml:"http://s3.amazonaws.com/doc/2006-03-01/ Delete"`
	Objects []struct {
		Key string `xml:"Key"`
	} `xml:"Object"`
}

func (m *mockS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/test-bucket")
	if path == "" {
		path = "/"
	}

	// ListObjectsV2 — the SDK sends GET /bucket?list-type=2&prefix=...
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		prefix := r.URL.Query().Get("prefix")
		var contents []s3ListEntry
		var keys []string
		for k := range m.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			contents = append(contents, s3ListEntry{Key: k, Size: int64(len(m.objects[k]))})
		}
		result := s3ListResult{
			Name:        "test-bucket",
			Prefix:      prefix,
			KeyCount:    len(contents),
			IsTruncated: false,
			Contents:    contents,
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		xml.NewEncoder(w).Encode(result)
		return
	}

	switch r.Method {
	case http.MethodHead:
		if path == "/" {
			// HeadBucket
			w.WriteHeader(http.StatusOK)
			return
		}
		// HeadObject
		key := strings.TrimPrefix(path, "/")
		content, ok := m.objects[key]
		if !ok {
			writeS3Error(w, "NoSuchKey", "The specified key does not exist.", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		key := strings.TrimPrefix(path, "/")
		content, ok := m.objects[key]
		if !ok {
			writeS3Error(w, "NoSuchKey", "The specified key does not exist.", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		w.Write(content)

	case http.MethodPut:
		key := strings.TrimPrefix(path, "/")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeS3Error(w, "InternalError", err.Error(), http.StatusInternalServerError)
			return
		}
		m.objects[key] = body
		w.WriteHeader(http.StatusOK)

	case http.MethodPost:
		// DeleteObjects is a POST with ?delete query
		if !r.URL.Query().Has("delete") {
			writeS3Error(w, "MethodNotAllowed", "The specified method is not allowed.", http.StatusMethodNotAllowed)
			return
		}
		var req s3DeleteRequest
		if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
			writeS3Error(w, "MalformedXML", err.Error(), http.StatusBadRequest)
			return
		}
		var result s3DeleteResult
		for _, obj := range req.Objects {
			delete(m.objects, obj.Key)
			result.Deleted = append(result.Deleted, s3DeletedEntry{Key: obj.Key})
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		xml.NewEncoder(w).Encode(result)

	case http.MethodDelete:
		key := strings.TrimPrefix(path, "/")
		delete(m.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

// newS3TestCache creates an S3Cache backed by an in-memory mock S3 server.
func newS3TestCache(t *testing.T) (*mockS3, cache.Cache) {
	t.Helper()
	m := newMockS3()

	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	require.NoError(t, err)

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(srv.URL + "/")
		o.UsePathStyle = true
	})

	c := cache.NewS3WithClient("test-bucket", client, t.TempDir())
	t.Cleanup(func() { c.Close() })
	return m, c
}

func TestS3_MetaRoundtrip(t *testing.T) {
	_, c := newS3TestCache(t)
	ctx := context.Background()
	data := []byte(`{"name":"lodash"}`)
	require.NoError(t, c.SetMeta(ctx, "npm/lodash/4.17.21", data, time.Hour))
	got, err := c.GetMeta(ctx, "npm/lodash/4.17.21")
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestS3_MetaMiss(t *testing.T) {
	_, c := newS3TestCache(t)
	got, err := c.GetMeta(context.Background(), "npm/doesnotexist/1.0.0")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestS3_MetaTTLExpiry(t *testing.T) {
	_, c := newS3TestCache(t)
	ctx := context.Background()
	require.NoError(t, c.SetMeta(ctx, "npm/old/1.0.0", []byte("data"), -time.Second))
	got, err := c.GetMeta(ctx, "npm/old/1.0.0")
	require.NoError(t, err)
	assert.Nil(t, got, "expired entry should be a miss")
}

func TestS3_BlobRoundtrip(t *testing.T) {
	_, c := newS3TestCache(t)
	ctx := context.Background()
	content := []byte("tarball-bytes")
	require.NoError(t, c.SetBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz", bytes.NewReader(content)))
	r, err := c.GetBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz")
	require.NoError(t, err)
	require.NotNil(t, r)
	defer r.Close()
	got, _ := io.ReadAll(r)
	assert.Equal(t, content, got)
}

func TestS3_BlobMiss(t *testing.T) {
	_, c := newS3TestCache(t)
	r, err := c.GetBlob(context.Background(), "npm/missing/1.0.0.tgz")
	require.NoError(t, err)
	assert.Nil(t, r)
}

func TestS3_HasBlob(t *testing.T) {
	_, c := newS3TestCache(t)
	ctx := context.Background()
	require.NoError(t, c.SetBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz", strings.NewReader("data")))
	assert.True(t, c.HasBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz"))
	assert.False(t, c.HasBlob(ctx, "npm/missing/1.0.0.tgz"))
}

func TestS3_BlobSize(t *testing.T) {
	_, c := newS3TestCache(t)
	ctx := context.Background()
	require.NoError(t, c.SetBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz", strings.NewReader("hello")))
	assert.Equal(t, int64(5), c.BlobSize(ctx, "npm/lodash/-/lodash-4.17.21.tgz"))
	assert.Equal(t, int64(-1), c.BlobSize(ctx, "npm/missing/1.0.0.tgz"))
}

func TestS3_Healthy(t *testing.T) {
	_, c := newS3TestCache(t)
	err := c.Healthy(context.Background())
	assert.NoError(t, err)
}

func TestS3_InvalidateMeta(t *testing.T) {
	_, c := newS3TestCache(t)
	ctx := context.Background()

	data := []byte(`{"name":"lodash"}`)
	require.NoError(t, c.SetMeta(ctx, "npm/lodash/4.17.21", data, time.Hour))
	require.NoError(t, c.SetMeta(ctx, "pypi/requests/2.31.0", data, time.Hour))

	// Set a blob that should survive invalidation.
	require.NoError(t, c.SetBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz", strings.NewReader("blob-data")))

	require.NoError(t, c.InvalidateMeta())

	// Metas should be gone.
	got, err := c.GetMeta(ctx, "npm/lodash/4.17.21")
	require.NoError(t, err)
	assert.Nil(t, got, "meta should be invalidated")

	got, err = c.GetMeta(ctx, "pypi/requests/2.31.0")
	require.NoError(t, err)
	assert.Nil(t, got, "meta should be invalidated")

	// Blob should survive.
	assert.True(t, c.HasBlob(ctx, "npm/lodash/-/lodash-4.17.21.tgz"), "blob must survive invalidation")
}

// TestS3_ConcurrentAccess exercises the cache from multiple goroutines to catch
// races in the temp-file upload / S3 API path. Each goroutine writes a unique
// blob, then reads all of them back.
func TestS3_ConcurrentAccess(t *testing.T) {
	_, c := newS3TestCache(t)
	ctx := context.Background()
	const workers = 10
	const items = 5

	errs := make(chan error, workers*items*2)
	for w := range workers {
		w := w
		go func() {
			for i := range items {
				key := fmt.Sprintf("concurrent/w%d/i%d.tgz", w, i)
				data := []byte(fmt.Sprintf("worker-%d-item-%d", w, i))
				if err := c.SetBlob(ctx, key, bytes.NewReader(data)); err != nil {
					errs <- fmt.Errorf("set %s: %w", key, err)
					return
				}
				r, err := c.GetBlob(ctx, key)
				if err != nil {
					errs <- fmt.Errorf("get %s: %w", key, err)
					return
				}
				if r == nil {
					errs <- fmt.Errorf("get %s: nil reader (missing)", key)
					return
				}
				got, _ := io.ReadAll(r)
				r.Close()
				if !bytes.Equal(got, data) {
					errs <- fmt.Errorf("get %s: got %q, want %q", key, string(got), string(data))
					return
				}
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for err := range errs {
			t.Error(err)
		}
	}()

	// Give all goroutines time to finish.
	time.Sleep(2 * time.Second)
	close(errs)
	<-done
}
