package meeting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func webSrcRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..", "apps", "web", "src")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("web src root: %v", err)
	}
	return root
}

func readWebFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(webSrcRoot(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 组织者才渲染「生成转写」；非组织者不渲染该按钮。
func TestWebNotesPageOrganizerOnlyGenerateTranscript(t *testing.T) {
	page := readWebFile(t, filepath.Join("pages", "NotesPage.tsx"))
	if !strings.Contains(page, "isOrganizer") {
		t.Fatal("NotesPage must gate 生成转写 on isOrganizer")
	}
	if !strings.Contains(page, "生成转写") {
		t.Fatal("NotesPage must include 生成转写 label")
	}
	if !strings.Contains(page, "showGenerateTranscript") {
		t.Fatal("NotesPage must compute showGenerateTranscript for organizer-only render")
	}
	// 按钮包裹在 isOrganizer 条件内（showGenerateTranscript）。
	if !strings.Contains(page, "{showGenerateTranscript ? (") {
		t.Fatal("生成转写 must render only when showGenerateTranscript is true")
	}
}

// 422 no_audio 空态不得发明假段落 / stub 文案。
func TestWebTranscriptPanelNoAudioHasNoFakeSegments(t *testing.T) {
	panel := readWebFile(t, filepath.Join("aura", "TranscriptPanel.tsx"))
	for _, bad := range []string{
		"【stub ASR】",
		"fake segment",
		"假转写",
		"lorem ipsum",
		"示例段落",
	} {
		if strings.Contains(panel, bad) {
			t.Fatalf("TranscriptPanel must not invent fake content %q", bad)
		}
	}
	if !strings.Contains(panel, "mode === 'no-audio'") {
		t.Fatal("TranscriptPanel must handle no-audio state")
	}
	if !strings.Contains(panel, "GeneratingSkeleton") {
		t.Fatal("TranscriptPanel must include GeneratingSkeleton")
	}
	if !strings.Contains(panel, "SegmentList") {
		t.Fatal("TranscriptPanel must include SegmentList")
	}
	if !strings.Contains(panel, "speaker_display_name") || !strings.Contains(panel, "start_ms") {
		t.Fatal("segment list must show speaker + t_ms (start_ms) + text")
	}
}

// 无转写时 NotesPanel 生成纪要动作保持 disabled。
func TestWebNotesPanelGenerateDisabledWithoutTranscript(t *testing.T) {
	panel := readWebFile(t, filepath.Join("aura", "NotesPanel.tsx"))
	page := readWebFile(t, filepath.Join("pages", "NotesPage.tsx"))
	if !strings.Contains(panel, "generateDisabled") {
		t.Fatal("NotesPanel must accept generateDisabled")
	}
	if !strings.Contains(panel, "生成纪要") {
		t.Fatal("NotesPanel must expose 生成纪要 action")
	}
	if !strings.Contains(page, "generateDisabled={!hasTranscript}") {
		t.Fatal("NotesPage must disable notes generate when there is no transcript")
	}
	// 转写成功后不得自动调 LLM。
	if strings.Contains(page, "await generateSummary") && strings.Contains(page, "handleGenerateTranscript") {
		// ensure generateSummary is not called inside handleGenerateTranscript
		start := strings.Index(page, "async function handleGenerateTranscript")
		end := strings.Index(page, "async function handleGenerateNotes")
		if start < 0 || end < 0 || end <= start {
			t.Fatal("expected handleGenerateTranscript and handleGenerateNotes")
		}
		body := page[start:end]
		if strings.Contains(body, "generateSummary") {
			t.Fatal("must not auto-call LLM/generateSummary after ASR generate")
		}
	}
}

// NotesPage 录像回放：getMedia + 三态；空态不得渲染 <video>。
func TestWebNotesPageRecordingPlaybackContract(t *testing.T) {
	page := readWebFile(t, filepath.Join("pages", "NotesPage.tsx"))
	panel := readWebFile(t, filepath.Join("aura", "ReplayPanel.tsx"))

	for _, needle := range []string{"getMedia", "ReplayPanel", "录像", "pickPlayableArtifact"} {
		if !strings.Contains(page, needle) {
			t.Fatalf("NotesPage must use recording playback via %q", needle)
		}
	}
	if !strings.Contains(page, "parseApiError") {
		t.Fatal("NotesPage must surface media errors via parseApiError")
	}
	if strings.Contains(panel, "poster=") || strings.Contains(panel, "placeholder") {
		t.Fatal("ReplayPanel must not invent fake poster/placeholder video")
	}
	if !strings.Contains(panel, "mode === 'empty'") {
		t.Fatal("ReplayPanel must handle empty state")
	}
	if !strings.Contains(panel, "download_url") {
		t.Fatal("ReplayPanel must require download_url before player")
	}
	if !strings.Contains(panel, "status === 'ready'") {
		t.Fatal("ReplayPanel must only play ready artifacts")
	}
	if !strings.Contains(panel, "LoadingSkeleton") {
		t.Fatal("ReplayPanel must use LoadingSkeleton for loading state")
	}
	if strings.Contains(panel, "bg-black") {
		t.Fatal("ReplayPanel video canvas must not use bg-black")
	}
	if !strings.Contains(panel, "bg-[#0A0D12]") {
		t.Fatal("ReplayPanel video canvas must use bg-[#0A0D12]")
	}
	emptyIdx := strings.Index(panel, "mode === 'empty'")
	videoIdx := strings.Index(panel, "<video")
	if emptyIdx < 0 || videoIdx < 0 {
		t.Fatal("expected empty branch and video player")
	}
	if emptyIdx > videoIdx {
		t.Fatal("empty state branch must be checked before rendering <video>")
	}
	// 录像区块应在转写与纪要之后。
	transcriptIdx := strings.Index(page, ">转写</h2>")
	notesIdx := strings.Index(page, ">纪要</h2>")
	recordingIdx := strings.Index(page, ">录像</h2>")
	if transcriptIdx < 0 || notesIdx < 0 || recordingIdx < 0 {
		t.Fatal("NotesPage must include 转写, 纪要, and 录像 section headings")
	}
	if !(transcriptIdx < notesIdx && notesIdx < recordingIdx) {
		t.Fatal("录像 section must appear below transcript and notes")
	}
}

func TestGatewaySourceHasNoRunASRStub(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "services", "gateway")
	var hits []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		// 测试名里允许提到已删除路由。
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		if strings.Contains(text, "RunASRStub") || strings.Contains(text, "run-asr-stub") {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("non-test gateway code must not retain RunASRStub / run-asr-stub: %v", hits)
	}
}
