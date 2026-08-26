package meeting

import (
	"cmp"
	"slices"
	"strings"
)

func genericSpeakerName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case "", "speaker", "organizer", "participant", "组织者", "参会人":
		return true
	}
	return strings.HasPrefix(n, "说话人") || strings.HasPrefix(n, "speaker-") || strings.HasPrefix(n, "speaker")
}

func genericSpeakerID(id string) bool {
	n := strings.ToLower(strings.TrimSpace(id))
	switch n {
	case "", "speaker", "organizer", "participant", "speaker-1", "speaker-2":
		return true
	}
	return strings.HasPrefix(n, "speaker")
}

// BindTranscriptSpeakers 把转写片段绑到入会时的显示名快照（架构 §3.2）。
func BindTranscriptSpeakers(members []MeetingMember, organizerID string, segs []TranscriptSegment) []TranscriptSegment {
	if len(segs) == 0 {
		return segs
	}
	byID := make(map[string]MeetingMember, len(members))
	byName := make(map[string]MeetingMember, len(members))
	for _, member := range members {
		byID[member.UserID] = member
		if name := strings.TrimSpace(member.DisplayNameSnapshot); name != "" {
			byName[strings.ToLower(name)] = member
		}
	}
	used := map[string]struct{}{}
	out := slices.Clone(segs)
	for i, seg := range out {
		if member, ok := byID[seg.SpeakerUserID]; ok && !genericSpeakerID(seg.SpeakerUserID) {
			out[i] = applyMemberSnapshot(seg, member)
			used[member.UserID] = struct{}{}
			continue
		}
		if member, ok := byName[strings.ToLower(strings.TrimSpace(seg.SpeakerDisplayName))]; ok {
			out[i] = applyMemberSnapshot(seg, member)
			used[member.UserID] = struct{}{}
			continue
		}
		if genericSpeakerID(seg.SpeakerUserID) || genericSpeakerName(seg.SpeakerDisplayName) {
			if member, ok := nextUnusedMember(members, organizerID, used); ok {
				out[i] = applyMemberSnapshot(seg, member)
				used[member.UserID] = struct{}{}
			}
		}
	}
	return out
}

func applyMemberSnapshot(seg TranscriptSegment, member MeetingMember) TranscriptSegment {
	seg.SpeakerUserID = member.UserID
	seg.SpeakerDisplayName = cmp.Or(strings.TrimSpace(member.DisplayNameSnapshot), member.UserID)
	return seg
}

func nextUnusedMember(members []MeetingMember, organizerID string, used map[string]struct{}) (MeetingMember, bool) {
	if organizerID != "" {
		if _, taken := used[organizerID]; !taken {
			for _, member := range members {
				if member.UserID == organizerID {
					return member, true
				}
			}
			return MeetingMember{UserID: organizerID, DisplayNameSnapshot: organizerID}, true
		}
	}
	for _, member := range members {
		if _, taken := used[member.UserID]; taken {
			continue
		}
		return member, true
	}
	return MeetingMember{}, false
}

func localFallbackKeys(arts []MediaArtifact) map[string]struct{} {
	out := map[string]struct{}{}
	legacy := false
	for _, art := range arts {
		if art.Kind != KindLocalMic || art.Status != "ready" {
			continue
		}
		key := strings.TrimSpace(art.ParticipantKey)
		if key == "" {
			legacy = true
			continue
		}
		out[key] = struct{}{}
		if uid, ok := strings.CutPrefix(key, "employee:"); ok {
			out[uid] = struct{}{}
		}
	}
	if legacy && len(out) == 0 {
		out["*"] = struct{}{}
	}
	return out
}

func segmentSourceForSpeaker(speakerUserID string, fallbacks map[string]struct{}, hasParticipantTrack bool) string {
	if hasParticipantTrack {
		return "egress"
	}
	if _, ok := fallbacks["*"]; ok {
		return "local_fallback"
	}
	if _, ok := fallbacks[speakerUserID]; ok {
		return "local_fallback"
	}
	if _, ok := fallbacks["employee:"+speakerUserID]; ok {
		return "local_fallback"
	}
	return "egress"
}
