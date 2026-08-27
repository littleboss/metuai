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
