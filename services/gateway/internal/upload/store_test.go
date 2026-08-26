package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestPutChunkAndFinalize(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	part1 := []byte("hello ")
	part2 := []byte("world")
	sum1 := sha256.Sum256(part1)
	sum2 := sha256.Sum256(part2)
	meta := ChunkMeta{MeetingID: "mtg_1", UserID: "u-1", UploadID: "up_1"}

	if err := s.PutChunk(ChunkMeta{
		MeetingID: meta.MeetingID, UserID: meta.UserID, UploadID: meta.UploadID,
		Index: 0, Checksum: hex.EncodeToString(sum1[:]),
	}, bytes.NewReader(part1)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChunk(ChunkMeta{
		MeetingID: meta.MeetingID, UserID: meta.UserID, UploadID: meta.UploadID,
		Index: 1, Checksum: hex.EncodeToString(sum2[:]),
	}, bytes.NewReader(part2)); err != nil {
		t.Fatal(err)
	}

	final, err := s.Finalize(meta, 2)
	if err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(append(part1, part2...))
	if final != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("final checksum %s want %s", final, hex.EncodeToString(wantSum[:]))
	}
	if got, ok := s.AckedChecksum("up_1"); !ok || got != final {
		t.Fatalf("ack = %q %v", got, ok)
	}
}

func TestStatusListsReceivedParts(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta := ChunkMeta{MeetingID: "mtg_1", UserID: "u-1", UploadID: "up_resume"}
	part := []byte("chunk-0")
	sum := sha256.Sum256(part)
	if err := s.PutChunk(ChunkMeta{
		MeetingID: meta.MeetingID, UserID: meta.UserID, UploadID: meta.UploadID,
		Index: 0, Checksum: hex.EncodeToString(sum[:]),
	}, bytes.NewReader(part)); err != nil {
		t.Fatal(err)
	}
	part2 := []byte("chunk-2")
	sum2 := sha256.Sum256(part2)
	if err := s.PutChunk(ChunkMeta{
		MeetingID: meta.MeetingID, UserID: meta.UserID, UploadID: meta.UploadID,
		Index: 2, Checksum: hex.EncodeToString(sum2[:]),
	}, bytes.NewReader(part2)); err != nil {
		t.Fatal(err)
	}

	st, err := s.Status(meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Received) != 2 || st.Received[0] != 0 || st.Received[1] != 2 {
		t.Fatalf("want received [0,2], got %+v", st.Received)
	}
	if st.Finalized {
		t.Fatal("should not be finalized yet")
	}
}

func TestChecksumMismatch(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = s.PutChunk(ChunkMeta{
		MeetingID: "m", UserID: "u", UploadID: "up", Index: 0, Checksum: "deadbeef",
	}, bytes.NewReader([]byte("x")))
	if err == nil || err.Error() != "checksum_mismatch" {
		t.Fatalf("want checksum_mismatch, got %v", err)
	}
}
