package config

import (
	"testing"
	"time"
)

// A bare CACHE_TTL number is hours, and hours overflow a time.Duration at
// 2562048 of them. "3000000" wrapped to a negative duration that ValidateKey
// accepted: the cache then stored entries that were already expired, Get
// never hit one again, and a credential backend asked for the master password
// every single time with nothing to show why.
func TestParseCacheTTLRefusesWhatCannotBeADuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"8h", 8 * time.Hour, true},
		{"45m", 45 * time.Minute, true},
		{"0", 0, true},
		{"2562047", 2562047 * time.Hour, true}, // the last hour count that fits
		{"2562048", 0, false},                  // one more wraps
		{"3000000", 0, false},
		{"-5", 0, false},
		{"-8h", 0, false},
		{"9223372036854775807", 0, false},
	}
	for _, c := range cases {
		got, err := ParseCacheTTL(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("ParseCacheTTL(%q): %v", c.in, err)
				continue
			}
			if got != c.want {
				t.Errorf("ParseCacheTTL(%q) = %v, want %v", c.in, got, c.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseCacheTTL(%q) = %v, want an error", c.in, got)
		}
		if got < 0 {
			t.Errorf("ParseCacheTTL(%q) = %v, a negative duration expires on arrival", c.in, got)
		}
	}
}

// The validator has to refuse the same values, or the file keeps a setting
// that silently turns the cache off.
func TestValidateKeyRefusesAnOverflowingCacheTTL(t *testing.T) {
	for _, v := range []string{"3000000", "-5", "-8h", "2562048"} {
		if err := ValidateKey("CACHE_TTL", v); err == nil {
			t.Errorf("ValidateKey(CACHE_TTL, %q) accepted it", v)
		}
	}
	for _, v := range []string{"8h", "45m", "reboot", "forever", "0"} {
		if err := ValidateKey("CACHE_TTL", v); err != nil {
			t.Errorf("ValidateKey(CACHE_TTL, %q): %v", v, err)
		}
	}
}
