package sync

import (
	"time"

	db "github.com/lucasbouet/spotlab-go/internal/db/gen"
)

// DeviceDTOsFromRows converts DB rows into the wire shape Subscribe and
// BroadcastDevices expect, with Online left false — the Hub fills that in
// itself from its own connection state, since only the actor goroutine may
// read it. Shared by the SSE connect handler and main.go's wiring of
// devices.BroadcastFunc, so the conversion exists exactly once.
func DeviceDTOsFromRows(rows []db.Device) []DeviceDTO {
	dtos := make([]DeviceDTO, len(rows))
	for i, d := range rows {
		dtos[i] = DeviceDTO{
			DeviceID:   d.DeviceID,
			Name:       d.Name,
			Platform:   d.Platform,
			LastSeenAt: d.LastSeenAt.UTC().Format(time.RFC3339),
		}
	}
	return dtos
}
