package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Cache struct {
	client *s3.Client
	bucket string
}

func NewS3(bucket, region, endpoint string) (*S3Cache, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	var clientOpts []func(*s3.Options)
	if endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
	}

	return &S3Cache{client: s3.NewFromConfig(cfg, clientOpts...), bucket: bucket}, nil
}

func (s *S3Cache) metaKey(key string) string {
	return "meta/" + key + ".json"
}

func (s *S3Cache) blobKey(key string) string {
	return "blobs/" + key
}

func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	return errors.As(err, &nsk) || strings.Contains(err.Error(), "NoSuchKey")
}

func (s *S3Cache) GetMeta(ctx context.Context, key string) ([]byte, error) {
	k := s.metaKey(key)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	defer out.Body.Close()
	var entry metaEntry
	if err := json.NewDecoder(out.Body).Decode(&entry); err != nil {
		return nil, nil
	}
	if time.Now().After(entry.ExpiresAt) {
		return nil, nil
	}
	return entry.Data, nil
}

func (s *S3Cache) SetMeta(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	entry := metaEntry{ExpiresAt: time.Now().Add(ttl), Data: data}
	encoded, _ := json.Marshal(entry)
	k := s.metaKey(key)
	ct := aws.String("application/json")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &k,
		Body:        bytes.NewReader(encoded),
		ContentType: ct,
	})
	return err
}

func (s *S3Cache) GetBlob(ctx context.Context, key string) (io.ReadCloser, error) {
	k := s.blobKey(key)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &k})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return out.Body, nil
}

func (s *S3Cache) SetBlob(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	k := s.blobKey(key)
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &k,
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *S3Cache) Close() error { return nil }
