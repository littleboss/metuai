package upload

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"time"
)

// S3Config 是网关自己 PutObject 用的凭证（不是 LiveKit Egress 容器里那份）。
type S3Config struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}

func (c S3Config) Ready() bool {
	return c.Endpoint != "" &&
		c.Bucket != "" &&
		c.AccessKey != "" &&
		c.SecretKey != ""
}

// S3BlobStore 用 path-style 访问 MinIO / 兼容 S3。
type S3BlobStore struct {
	client *s3.Client
	bucket string
}

// NewS3BlobStore 构造客户端。cfg 不齐时返回 (nil, nil)，调用方按「未接线」处理。
func NewS3BlobStore(cfg S3Config) (*S3BlobStore, error) {
	if !cfg.Ready() {
		return nil, nil
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	client := s3.New(s3.Options{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: cfg.ForcePathStyle,
	})
	return &S3BlobStore{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3BlobStore) Enabled() bool {
	return s != nil && s.client != nil && s.bucket != ""
}

func (s *S3BlobStore) PutFile(ctx context.Context, objectKey, localPath, contentType string) error {
	if !s.Enabled() {
		return fmt.Errorf("s3 blob store disabled")
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		Body:          f,
		ContentLength: aws.Int64(stat.Size()),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("s3 put %s/%s: %w", s.bucket, objectKey, err)
	}
	return nil
}

func (s *S3BlobStore) SignGetURL(ctx context.Context, objectKey string, ttl time.Duration) (string, error) {
	if !s.Enabled() {
		return "", fmt.Errorf("s3 blob store disabled")
	}
	objectKey = strings.TrimSpace(objectKey)
	if objectKey == "" {
		return "", fmt.Errorf("object key required")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	presign := s3.NewPresignClient(s.client)
	out, err := presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3 presign get %s/%s: %w", s.bucket, objectKey, err)
	}
	return out.URL, nil
}

func (s *S3BlobStore) DeleteObject(ctx context.Context, objectKey string) error {
	if !s.Enabled() {
		return fmt.Errorf("s3 blob store disabled")
	}
	objectKey = strings.TrimSpace(objectKey)
	if objectKey == "" {
		return nil
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("s3 delete %s/%s: %w", s.bucket, objectKey, err)
	}
	return nil
}

// ResolveUploadEndpoint 选网关能连上的 MinIO 地址。
// Egress 容器要用 http://minio:9000；网关在宿主机上应走 S3_UPLOAD_ENDPOINT（默认 127.0.0.1:19000）。
func ResolveUploadEndpoint(egressEndpoint, uploadEndpoint string) string {
	if strings.TrimSpace(uploadEndpoint) != "" {
		return strings.TrimSpace(uploadEndpoint)
	}
	return strings.TrimSpace(egressEndpoint)
}
