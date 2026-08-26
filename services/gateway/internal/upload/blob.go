package upload

import "context"

// BlobStore 把合并后的本机录音回传到对象存储（MinIO / S3）。
// nil 或不 Enabled 时 complete 仍写本地 spool + ready 元数据（PoC 降级）。
type BlobStore interface {
	Enabled() bool
	// PutFile 上传本地文件；objectKey 不含桶名（例如 local-recording/mtg/.../merged.bin）。
	PutFile(ctx context.Context, objectKey, localPath, contentType string) error
}

// NoopBlobStore 显式关闭回传，单测与未配 S3 时使用。
type NoopBlobStore struct{}

func (NoopBlobStore) Enabled() bool { return false }

func (NoopBlobStore) PutFile(context.Context, string, string, string) error { return nil }
