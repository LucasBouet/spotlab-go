// Package devices implements device registration/listing/rename/forget —
// /api/devices/**. Presence ("online") and the post-mutation broadcast both
// belong to the in-memory sync engine (Phase 6); until it exists, this
// package takes them as injected functions (see Mount) so this file never
// has to change once the real Hub lands — only main.go's wiring does.
package devices

import "time"

// DeviceDTO mirrors the Android client's DeviceDto exactly
// (data/remote/dto/SyncDto.kt).
type DeviceDTO struct {
	DeviceID   string `json:"deviceId"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	Online     bool   `json:"online"`
	LastSeenAt string `json:"lastSeenAt"`
}

func deviceDTO(deviceID, name, platform string, lastSeenAt time.Time, online bool) DeviceDTO {
	return DeviceDTO{
		DeviceID:   deviceID,
		Name:       name,
		Platform:   platform,
		Online:     online,
		LastSeenAt: lastSeenAt.Format(time.RFC3339),
	}
}
