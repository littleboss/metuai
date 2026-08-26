// Package upload 承接员工 Tauri 本机麦克风分块上传（断点续传）。
// 流程：校验 checksum → 合并本地 spool →（可选）PutObject 到 MinIO。
package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type ChunkMeta struct {
	MeetingID string
	UserID    string
	UploadID  string
	Index     int
	Checksum  string // sha256 hex of chunk bytes
}

type Store struct {
	mu   sync.Mutex
	root string
	acks map[string]string // uploadID -> final checksum
}

func NewStore(root string) (*Store, error) {
	if root == "" {
		root = filepath.Join(os.TempDir(), "metuai-uploads")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: root, acks: map[string]string{}}, nil
}

func (s *Store) dirFor(meta ChunkMeta) string {
	return filepath.Join(s.root, meta.MeetingID, meta.UserID, meta.UploadID)
}

// MergedPath 是 Finalize 合并后的本地文件路径；回传 MinIO 成功后仍保留作 spool。
func (s *Store) MergedPath(meta ChunkMeta) string {
	return filepath.Join(s.dirFor(meta), "merged.bin")
}

// ObjectKey 是桶内对象键（不含桶名），与 Egress 产物同一套路径习惯。
func (s *Store) ObjectKey(meta ChunkMeta) string {
	return filepath.ToSlash(filepath.Join(
		"local-recording", meta.MeetingID, meta.UserID, meta.UploadID, "merged.bin",
	))
}

// QualifiedObjectKey 写成 bucket/key，便于会后页与 Egress 产物并排展示。
func (s *Store) QualifiedObjectKey(bucket string, meta ChunkMeta) string {
	key := s.ObjectKey(meta)
	if bucket == "" {
		return key
	}
	return bucket + "/" + key
}

func (s *Store) PutChunk(meta ChunkMeta, body io.Reader) error {
	if meta.MeetingID == "" || meta.UserID == "" || meta.UploadID == "" || meta.Checksum == "" {
		return fmt.Errorf("meeting_id, user_id, upload_id, checksum required")
	}
	dir := s.dirFor(meta)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != meta.Checksum {
		return fmt.Errorf("checksum_mismatch")
	}
	path := filepath.Join(dir, fmt.Sprintf("%06d.part", meta.Index))
	return os.WriteFile(path, data, 0o600)
}

func (s *Store) Finalize(meta ChunkMeta, expectedParts int) (finalChecksum string, err error) {
	dir := s.dirFor(meta)
	outPath := filepath.Join(dir, "merged.bin")
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()

	hasher := sha256.New()
	for i := 0; i < expectedParts; i++ {
		partPath := filepath.Join(dir, fmt.Sprintf("%06d.part", i))
		part, err := os.ReadFile(partPath)
		if err != nil {
			return "", fmt.Errorf("missing_part_%d", i)
		}
		if _, err := out.Write(part); err != nil {
			return "", err
		}
		_, _ = hasher.Write(part)
	}
	finalChecksum = hex.EncodeToString(hasher.Sum(nil))
	s.mu.Lock()
	s.acks[meta.UploadID] = finalChecksum
	s.mu.Unlock()
	return finalChecksum, nil
}

func (s *Store) AckedChecksum(uploadID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.acks[uploadID]
	return v, ok
}

// UploadStatus 供断点续传：客户端可跳过已收到的分块，或发现已 complete。
type UploadStatus struct {
	Received   []int  `json:"received"`
	Finalized  bool   `json:"finalized"`
	Checksum   string `json:"checksum,omitempty"`
	ObjectKey  string `json:"object_key,omitempty"`
	MergedPath string `json:"-"`
}

// Status 扫描 spool 目录里已落盘的 *.part；若已 Finalize 则带 checksum。
func (s *Store) Status(meta ChunkMeta) (UploadStatus, error) {
	if meta.MeetingID == "" || meta.UserID == "" || meta.UploadID == "" {
		return UploadStatus{}, fmt.Errorf("meeting_id, user_id, upload_id required")
	}
	out := UploadStatus{Received: []int{}, ObjectKey: s.ObjectKey(meta), MergedPath: s.MergedPath(meta)}
	dir := s.dirFor(meta)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) != 11 || !strings.HasSuffix(name, ".part") {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(name, "%06d.part", &idx); err != nil {
			continue
		}
		out.Received = append(out.Received, idx)
	}
	sort.Ints(out.Received)
	if sum, ok := s.AckedChecksum(meta.UploadID); ok {
		out.Finalized = true
		out.Checksum = sum
	} else if _, err := os.Stat(s.MergedPath(meta)); err == nil {
		// 进程重启后 acks 内存丢失，但 merged.bin 仍在 —— 标 finalized，checksum 留给客户端重算或再 complete。
		out.Finalized = true
	}
	return out, nil
}
