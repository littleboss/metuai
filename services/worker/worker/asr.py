"""ASR 后端：stub（默认）或可选 FunASR。

切换方式：
- 默认 / ASR_BACKEND=stub：不装 funasr 也能跑，按文件时长估时间轴，写占位句。
- ASR_BACKEND=funasr：尝试 import funasr；失败则报错（不静默退回 stub，避免误以为跑了真模型）。
"""

from __future__ import annotations

import logging
import os
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

log = logging.getLogger("metuai.asr")

# 粤语书面里较常见、普通话正文较少连用的字。命中若干个才标 yue。
_CANTONESE_CUES = set("嘅唔佢喺咗係冇啲咁喎嚟哋嗰㗎噉")


def detect_spoken_language(text: str) -> str:
    """PoC 启发式，不是 FunASR 语言识别。返回 zh-CN / yue / en。"""
    han = yue = latin = 0
    for ch in text or "":
        if "\u4e00" <= ch <= "\u9fff":
            han += 1
            if ch in _CANTONESE_CUES:
                yue += 1
        elif "A" <= ch <= "Z" or "a" <= ch <= "z":
            latin += 1
    if yue >= 2 or (han >= 4 and yue > 0 and yue * 100 // han >= 8):
        return "yue"
    if latin >= 8 and latin > han * 2:
        return "en"
    if han > 0:
        return "zh-CN"
    if latin > 0:
        return "en"
    return "zh-CN"


@dataclass
class Segment:
    track_id: str
    speaker_user_id: str
    speaker_display_name: str
    language: str
    start_ms: int
    end_ms: int
    text: str
    asr_model: str
    source: str
    confidence: float | None = None

    def to_api(self) -> dict[str, Any]:
        d = asdict(self)
        if d["confidence"] is None:
            del d["confidence"]
        return d


def resolve_backend(explicit: str | None = None) -> str:
    raw = (explicit or os.getenv("ASR_BACKEND") or "stub").strip().lower()
    if raw in ("funasr", "stub"):
        return raw
    raise ValueError(f"unsupported ASR_BACKEND={raw!r}; use stub|funasr")


def _estimate_duration_ms(path: Path) -> int:
    """粗估时长：pcm/s16le/48k mono → bytes/96；其它按文件大小瞎估。"""
    size = path.stat().st_size if path.is_file() else 0
    if size <= 0:
        return 3000
    name = path.name.lower()
    if name.endswith(".pcm") or name.endswith(".raw") or name.endswith(".bin"):
        # 48kHz * 2 bytes * 1ch = 96000 bytes/s
        return max(1000, int(size / 96))
    # ogg/mp4/wav：没有解码器时用「约 16KB/s」瞎估，仅供 stub 时间轴
    return max(1000, int(size / 16))


def transcribe_stub(
    audio_path: Path | None,
    *,
    source: str = "local_fallback",
    meeting_title: str = "",
) -> list[Segment]:
    """不依赖 FunASR 的占位转写；日志会明确写 stub。"""
    dur = _estimate_duration_ms(audio_path) if audio_path else 4000
    half = max(500, dur // 2)
    title = meeting_title or "本场会议"
    log.warning("ASR backend=stub (not FunASR); path=%s duration_ms≈%s", audio_path, dur)
    first = f"【stub ASR】关于「{title}」的讨论开始。"
    second = "【stub ASR】请切换 ASR_BACKEND=funasr 并安装 funasr 以启用真实转写。"
    return [
        Segment(
            track_id="stub-1",
            speaker_user_id="speaker-1",
            speaker_display_name="说话人1",
            language=detect_spoken_language(first),
            start_ms=0,
            end_ms=half,
            text=first,
            asr_model="stub-asr",
            source=source,
            confidence=None,
        ),
        Segment(
            track_id="stub-2",
            speaker_user_id="speaker-2",
            speaker_display_name="说话人2",
            language=detect_spoken_language(second),
            start_ms=half,
            end_ms=dur,
            text=second,
            asr_model="stub-asr",
            source=source,
            confidence=None,
        ),
    ]


def transcribe_funasr(audio_path: Path, *, source: str = "egress") -> list[Segment]:
    """调用 FunASR AutoModel。模型名由 FUNASR_MODEL 指定。"""
    try:
        from funasr import AutoModel  # type: ignore
    except ImportError as exc:
        raise RuntimeError(
            "ASR_BACKEND=funasr but funasr is not installed; "
            "pip install funasr  (or set ASR_BACKEND=stub)"
        ) from exc

    model_name = os.getenv("FUNASR_MODEL", "paraformer-zh")
    log.info("ASR backend=funasr model=%s path=%s", model_name, audio_path)
    model = AutoModel(model=model_name)
    result = model.generate(input=str(audio_path))
    # FunASR 返回结构随版本变化；尽量兼容 list[dict] / dict
    texts: list[str] = []
    if isinstance(result, list):
        for item in result:
            if isinstance(item, dict) and item.get("text"):
                texts.append(str(item["text"]))
            elif isinstance(item, str):
                texts.append(item)
    elif isinstance(result, dict) and result.get("text"):
        texts.append(str(result["text"]))
    elif isinstance(result, str):
        texts.append(result)

    if not texts:
        texts = ["（FunASR 未返回文本）"]

    segments: list[Segment] = []
    cursor = 0
    for i, text in enumerate(texts):
        end = cursor + max(1500, len(text) * 80)
        segments.append(
            Segment(
                track_id=f"funasr-{i}",
                speaker_user_id="speaker",
                speaker_display_name="说话人",
                language=detect_spoken_language(text),
                start_ms=cursor,
                end_ms=end,
                text=text.strip() or "（空）",
                asr_model=model_name,
                source=source,
                confidence=None,
            )
        )
        cursor = end + 200
    return segments


def transcribe_audio(
    audio_path: Path | None,
    *,
    backend: str | None = None,
    source: str = "local_fallback",
    meeting_title: str = "",
) -> tuple[str, list[Segment]]:
    """统一入口。返回 (backend_used, segments)。"""
    used = resolve_backend(backend)
    if used == "stub":
        return used, transcribe_stub(audio_path, source=source, meeting_title=meeting_title)
    if audio_path is None or not audio_path.is_file():
        raise FileNotFoundError("funasr backend requires an existing audio file")
    return used, transcribe_funasr(audio_path, source=source)
