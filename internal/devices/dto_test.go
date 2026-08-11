package devices

import (
	"testing"
	"time"
)

func TestDeviceDTOFormatsTimestampAsRFC3339(t *testing.T) {
	when := time.Date(2026, 8, 11, 19, 6, 4, 0, time.UTC)
	dto := deviceDTO("dev1", "Phone", "Android", when, true)
	if dto.LastSeenAt != "2026-08-11T19:06:04Z" {
		t.Errorf("LastSeenAt = %q", dto.LastSeenAt)
	}
	if !dto.Online {
		t.Error("Online = false, attendu true (passé explicitement)")
	}
}
