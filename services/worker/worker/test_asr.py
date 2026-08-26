"""ASR stub 与音源优先级测试（不依赖 FunASR/pytest）。"""

import tempfile
import unittest
from pathlib import Path

from worker.asr import detect_spoken_language, resolve_backend, transcribe_audio, transcribe_stub
from worker.main import _iter_audio_sources, _speaker_fields


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

    def test_iter_sources_covers_each_participant_and_skips_room_mix(self):
        artifacts = [
            {"kind": "room_audio", "status": "ready", "object_key": "room.ogg"},
            {
                "kind": "local_mic",
                "status": "ready",
                "object_key": "local-u1.pcm",
                "participant_key": "employee:u-1",
            },
            {
                "kind": "participant_track",
                "status": "ready",
                "object_key": "track-u2.ogg",
                "participant_key": "employee:u-2",
            },
            {
                "kind": "local_mic",
                "status": "ready",
                "object_key": "local-u2.pcm",
                "participant_key": "employee:u-2",
            },
        ]
        sources = _iter_audio_sources(artifacts)
        keys = {(s["kind"], s["participant_key"]) for s in sources}
        self.assertIn(("participant_track", "employee:u-2"), keys)
        self.assertIn(("local_mic", "employee:u-1"), keys)
        self.assertNotIn(("local_mic", "employee:u-2"), keys)
        self.assertTrue(all(s["kind"] != "room_audio" for s in sources))

    def test_room_mix_alone_is_not_a_source(self):
        self.assertEqual(
            _iter_audio_sources([{"kind": "room_audio", "status": "ready", "object_key": "room.ogg"}]),
            [],
        )

    def test_speaker_fields_from_participant_key(self):
        self.assertEqual(_speaker_fields("employee:u-1"), ("u-1", "u-1"))
        self.assertEqual(_speaker_fields(""), ("speaker", "说话人"))

    def test_detect_spoken_language_heuristic(self):
        self.assertEqual(detect_spoken_language("我们开始讨论明年预算。"), "zh-CN")
        self.assertEqual(detect_spoken_language("我哋而家喺会议室倾下预算嘅事。"), "yue")
        self.assertEqual(
            detect_spoken_language("Let's start with the budget review tomorrow."),
            "en",
        )


if __name__ == "__main__":
    unittest.main()
