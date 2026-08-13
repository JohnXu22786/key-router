package handler

import (
	"testing"
	"time"
)

// TestBuildActivityAxisDST pins the DST behavior of the activity bucket
// axis: the day/week axes must step calendar-wise (a 25-hour fall-back day
// must not duplicate a date label or skip a week) and the hour axis must
// merge the repeated wall-clock hour of a fall-back into one bucket.
// These cases cannot be exercised by the handler_test suite because the
// machine's local zone (China) has no DST.
func TestBuildActivityAxisDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// Nov 1 2026 02:00 EDT falls back to 01:00 EST.
	since := time.Date(2026, 10, 30, 0, 0, 0, 0, ny)
	until := time.Date(2026, 11, 3, 0, 0, 0, 0, ny)

	day := buildActivityAxis(since, until, "day")
	if !hasUnique(day) {
		t.Fatalf("day axis has duplicate labels across fall-back: %v", day)
	}
	if day[0] != "2026-10-30" || day[len(day)-1] != "2026-11-03" {
		t.Fatalf("day axis bounds = %v..%v, want 2026-10-30..2026-11-03", day[0], day[len(day)-1])
	}

	// The repeated 01:00 (EDT then EST) must collapse into ONE bucket; the
	// 02:00 that follows is a real EST hour and stays a single bucket.
	hour := buildActivityAxis(since, until, "hour")
	if n := count(hour, "2026-11-01 01:00"); n != 1 {
		t.Fatalf("hour axis has %d buckets labeled 2026-11-01 01:00, want 1: %v", n, hour)
	}
	if n := count(hour, "2026-11-01 02:00"); n != 1 {
		t.Fatalf("hour axis has %d buckets labeled 2026-11-01 02:00, want 1: %v", n, hour)
	}

	// Spring forward (Mar 8 2026): the nonexistent 02:00 must never appear.
	springSince := time.Date(2026, 3, 7, 0, 0, 0, 0, ny)
	springUntil := time.Date(2026, 3, 9, 0, 0, 0, 0, ny)
	spring := buildActivityAxis(springSince, springUntil, "hour")
	if n := count(spring, "2026-03-08 02:00"); n != 0 {
		t.Fatalf("hour axis contains nonexistent spring-forward 02:00: %v", spring)
	}
	if n := count(spring, "2026-03-08 03:00"); n != 1 {
		t.Fatalf("hour axis missing the 03:00 after the spring gap: %v", spring)
	}

	// Weeks step Monday to Monday and never skip the Nov 2 week.
	week := buildActivityAxis(since, until, "week")
	if week[0] != "2026-10-26" || week[len(week)-1] != "2026-11-02" {
		t.Fatalf("week axis = %v, want 2026-10-26 .. 2026-11-02", week)
	}
}

// TestBuildActivityAxisLocalHourFloor: the hour axis must floor to the
// LOCAL hour like billing's hour_bucket truncation, not to a UTC hour
// (which would misalign in half-hour-offset zones like +05:30).
func TestBuildActivityAxisLocalHourFloor(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata") // UTC+05:30
	if err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 8, 13, 15, 30, 0, 0, kolkata)
	axis := buildActivityAxis(since, since.Add(2*time.Hour), "hour")
	if len(axis) != 3 || axis[0] != "2026-08-13 15:00" || axis[2] != "2026-08-13 17:00" {
		t.Fatalf("hour axis = %v, want 15:00..17:00 local", axis)
	}
}

func hasUnique(s []string) bool {
	seen := make(map[string]bool, len(s))
	for _, v := range s {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func count(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
