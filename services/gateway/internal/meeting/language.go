package meeting

import (
	"strings"
	"unicode"
)

// 粤语书面里很常见、普通话正文较少单独连用的字。命中若干个才标 yue。
const cantoneseCueRunes = "嘅唔佢喺咗係冇啲咁喎嚟哋嗰㗎噉"

// DetectSpokenLanguage 是 PoC 启发式，不是 FunASR/WhisperX 语言识别。
// 根据汉字、粤语常用字和拉丁字母，给转写片段填 zh-CN / yue / en。
func DetectSpokenLanguage(text string) string {
	var han, yue, latin int
	cues := []rune(cantoneseCueRunes)
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
			for _, cue := range cues {
				if r == cue {
					yue++
					break
				}
			}
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			latin++
		}
	}
	switch {
	case yue >= 2 || (han >= 4 && yue > 0 && yue*100/han >= 8):
		return "yue"
	case latin >= 8 && latin > han*2:
		return "en"
	case han > 0:
		return "zh-CN"
	case latin > 0:
		return "en"
	default:
		return "zh-CN"
	}
}

func languageOrDetect(explicit, text string) string {
	if lang := strings.TrimSpace(explicit); lang != "" {
		return lang
	}
	return DetectSpokenLanguage(text)
}
