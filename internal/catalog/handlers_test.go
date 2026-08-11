package catalog

import "testing"

func TestClampPositiveInt(t *testing.T) {
	cases := []struct {
		raw      string
		fallback int
		max      int
		want     int
	}{
		{"", 24, 100, 24},        // absent -> fallback
		{"0", 24, 100, 24},       // zero -> fallback, not zero itself
		{"-5", 24, 100, 24},      // negative -> fallback
		{"garbage", 24, 100, 24}, // unparsable -> fallback
		{"10", 24, 100, 10},      // within range -> as given
		{"500", 24, 100, 100},    // over max -> clamped
		{"100", 24, 100, 100},    // exactly max -> unchanged
	}
	for _, c := range cases {
		got := clampPositiveInt(c.raw, c.fallback, c.max)
		if got != c.want {
			t.Errorf("clampPositiveInt(%q, %d, %d) = %d, attendu %d", c.raw, c.fallback, c.max, got, c.want)
		}
	}
}

func TestParsePositiveInt(t *testing.T) {
	cases := map[string]int{
		"":        0,
		"0":       0,
		"-1":      0,
		"garbage": 0,
		"5":       5,
	}
	for raw, want := range cases {
		if got := parsePositiveInt(raw); got != want {
			t.Errorf("parsePositiveInt(%q) = %d, attendu %d", raw, got, want)
		}
	}
}
