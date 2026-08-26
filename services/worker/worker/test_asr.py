"""ASR stub 与音源优先级测试（不依赖 FunASR/pytest）。"""

import tempfile
import unittest
from pathlib import Path

from worker.asr import resolve_backend, transcribe_audio, transcribe_stub
from worker.main import _pick_media_source


class ASRTest(unittest.TestCase):
    def test_resolve_backend_default(self):
        self.assertIn(resolve_backend(None), ("stub", "funasr"))

    def test_stub_produces_segments(self):
        with tempfile.TemporaryDirectory() as tmp:
            audio = Path(tmp) / "x.pcm"
            audio.write_bytes(b"\x00" * 96000)  # ~1s @ 48k s16le mono
            segs = transcribe_stub(audio, source="local_fallback", meeting_title="测试")
            self.assertEqual(len(segs), 2)
            self.assertEqual(segs[0].source, "local_fallback")
            self.assertIn("stub", segs[0].asr_model)
            used, out = transcribe_audio(audio, backend="stub", source="egress")
            self.assertEqual(used, "stub")
            self.assertGreaterEqual(len(out), 1)

    def test_authoritative_track_precedes_local_fallback_and_room_mix(self):
        artifacts = [
            {"kind": "room_audio", "status": "ready", "object_key": "room.ogg"},
            {"kind": "local_mic", "status": "ready", "object_key": "local.pcm"},
            {"kind": "participant_track", "status": "ready", "object_key": "track.ogg"},
        ]
        self.assertEqual(_pick_media_source(artifacts), ("participant_track", "track.ogg"))
        self.assertEqual(
            _pick_media_source(artifacts[:2]),
            ("local_mic", "local.pcm"),
        )
        self.assertEqual(_pick_media_source(artifacts[:1]), ("", ""))


if __name__ == "__main__":
    unittest.main()
