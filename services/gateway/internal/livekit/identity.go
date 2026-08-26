package livekit

import (
	"strings"
	"unicode"
)

const deviceSep = "~"

// DeviceIdentity 让同一员工多端同时在房。
// LiveKit 对同一 identity 会踢掉旧连接，所以观看端必须带设备后缀。
func DeviceIdentity(base, deviceID string) string {
	base = strings.TrimSpace(base)
	deviceID = sanitizeDeviceID(deviceID)
	if base == "" || deviceID == "" {
		return base
	}
	return base + deviceSep + deviceID
}

// UserKey 去掉设备后缀，得到踢人/抢麦用的稳定用户键。
func UserKey(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	base, _, found := strings.Cut(identity, deviceSep)
	if !found || strings.TrimSpace(base) == "" {
		return identity
	}
	return base
}

func sanitizeDeviceID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
		if b.Len() >= 32 {
			break
		}
	}
	return b.String()
}
