package handler

import (
	"testing"
	"time"
)

// TestBucketBound pins the hour-bucket upper bound used by
// /api/stats/consumptions' cap check. The pre-fix version used the raw
// sinceTime, which undercounted the inclusive set {hour-starts <= until}
// by one bucket whenever since was mid-hour and until was exactly on an
// hour boundary — the hairline window where the cap contract could be
// silently violated (the endpoint would exceed the row contract without
// setting the X-Consumptions-Truncated flag).
func TestBucketBound(t *testing.T) {
	mk := func(y, mo, d, h, mi int) time.Time {
		return time.Date(y, time.Month(mo), d, h, mi, 0, 0, time.UTC)
	}
	cases := []struct {
		name         string
		since, until time.Time
		want         int64
	}{
		{"both on grid same hour", mk(2026, 1, 1, 16, 0), mk(2026, 1, 1, 16, 0), 1},
		{"both on grid two hours", mk(2026, 1, 1, 16, 0), mk(2026, 1, 1, 18, 0), 3},
		{"mid-hour since, until on hour boundary (the bug case)",
			mk(2026, 1, 1, 16, 31), mk(2026, 1, 1, 18, 0), 3},
		{"mid-hour since, until mid-hour (floored 16:00 -> 3 buckets match)",
			mk(2026, 1, 1, 16, 31), mk(2026, 1, 1, 18, 29), 3},
		{"on-grid since, mid-hour until",
			mk(2026, 1, 1, 16, 0), mk(2026, 1, 1, 18, 30), 3},
		{"four-hour custom window",
			mk(2026, 1, 1, 16, 15), mk(2026, 1, 1, 20, 0), 5},
		{"reversed window returns 0",
			mk(2026, 1, 1, 18, 0), mk(2026, 1, 1, 16, 0), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := bucketBound(c.since, c.until); got != c.want {
				t.Errorf("bucketBound(%s, %s) = %d, want %d",
					c.since.Format(time.RFC3339), c.until.Format(time.RFC3339), got, c.want)
			}
		})
	}
}
