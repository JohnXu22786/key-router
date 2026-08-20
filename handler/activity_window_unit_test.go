package handler

import (
	"testing"
	"time"
)

// TestActivityWindow pins activityWindow's bucket widening for every rollup:
// `from` must be the bucket start containing since (a mid-hour since floors
// to the LOCAL hour), and `to` must be the first bucket start AFTER until so
// the bucket containing until is complete. The month branch (year rollover)
// is only exercised here; the hour/day branches are also covered through the
// handler_test suite, and the week branch through
// TestActivityWeekRollupMondayAlignment.
func TestActivityWindow(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 8, 13, 15, 30, 0, 0, loc)
	until := time.Date(2026, 8, 13, 17, 5, 0, 0, loc)

	from, to := activityWindow(since, until, "hour")
	if !from.Equal(time.Date(2026, 8, 13, 15, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2026, 8, 13, 18, 0, 0, 0, loc)) {
		t.Fatalf("hour window = %v..%v, want 15:00..18:00", from, to)
	}

	from, to = activityWindow(since, until, "day")
	if !from.Equal(time.Date(2026, 8, 13, 0, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2026, 8, 14, 0, 0, 0, 0, loc)) {
		t.Fatalf("day window = %v..%v, want Aug 13 00:00..Aug 14 00:00", from, to)
	}

	// Week: since is a Thursday, until a Saturday — both inside the same
	// Monday-anchored week; `to` must open the NEXT Monday.
	weekSince := time.Date(2026, 8, 13, 10, 0, 0, 0, loc) // Thursday
	weekUntil := time.Date(2026, 8, 15, 10, 0, 0, 0, loc) // Saturday
	from, to = activityWindow(weekSince, weekUntil, "week")
	if !from.Equal(time.Date(2026, 8, 10, 0, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2026, 8, 17, 0, 0, 0, 0, loc)) {
		t.Fatalf("week window = %v..%v, want Aug 10..Aug 17", from, to)
	}

	// Month: from = Aug 1, to = the 1st after until's month (year rollover).
	from, to = activityWindow(time.Date(2026, 8, 13, 0, 0, 0, 0, loc), time.Date(2026, 12, 31, 23, 0, 0, 0, loc), "month")
	if !from.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("month window = %v..%v, want Aug 1 2026..Jan 1 2027", from, to)
	}

	// until exactly on a bucket start: `to` stays exclusive so the boundary
	// bucket (16:00) is still included.
	from, to = activityWindow(since, time.Date(2026, 8, 13, 16, 0, 0, 0, loc), "hour")
	if !from.Equal(time.Date(2026, 8, 13, 15, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2026, 8, 13, 17, 0, 0, 0, loc)) {
		t.Fatalf("hour window at exact boundary = %v..%v, want 15:00..17:00", from, to)
	}

	// Total: the whole range collapses into one "Total" bucket, so its window
	// is [since, until] widened to the LOCAL-hour bucket boundaries of the
	// endpoints — the same floor/ceil as the hour branch. (Without the
	// explicit case it fell through to the month branch and widened a
	// mid-month range to a whole month.)
	from, to = activityWindow(since, until, "total")
	if !from.Equal(time.Date(2026, 8, 13, 15, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2026, 8, 13, 18, 0, 0, 0, loc)) {
		t.Fatalf("total window = %v..%v, want 15:00..18:00 (same as hour)", from, to)
	}
}

// TestActivityWindowTotalSpansRange pins the reported rollup=total bug
// (default 7-day window): the total window must span exactly the requested
// since..until (widened to the endpoint hour buckets), NOT the whole
// containing months. Before the fix, rollup=total fell through to the month
// branch and widened since=2026-08-13 .. until=2026-08-20 to
// 2026-08-01 .. 2026-09-01, pulling out-of-range usage into the Total bucket.
func TestActivityWindowTotalSpansRange(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 8, 13, 5, 30, 0, 0, loc)
	until := time.Date(2026, 8, 20, 16, 5, 0, 0, loc)
	from, to := activityWindow(since, until, "total")
	if wantF, wantT := time.Date(2026, 8, 13, 5, 0, 0, 0, loc), time.Date(2026, 8, 20, 17, 0, 0, 0, loc); !from.Equal(wantF) || !to.Equal(wantT) {
		t.Fatalf("total window = %v..%v, want %v..%v (the requested range, not Aug 1..Sep 1)", from, to, wantF, wantT)
	}
}

// TestActivityWindowLocalHourFloor: the hour floor must follow the LOCAL
// hour like billing's hour_bucket truncation, not UTC hours (which would
// misalign in half-hour-offset zones).
func TestActivityWindowLocalHourFloor(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata") // UTC+05:30
	if err != nil {
		t.Fatal(err)
	}
	// 15:30 local = 10:00 UTC; a UTC truncate would open at 10:00, the local
	// floor must open at 15:00.
	since := time.Date(2026, 8, 13, 15, 30, 0, 0, kolkata)
	from, to := activityWindow(since, since.Add(2*time.Hour), "hour")
	if !from.Equal(time.Date(2026, 8, 13, 15, 0, 0, 0, kolkata)) ||
		!to.Equal(time.Date(2026, 8, 13, 18, 0, 0, 0, kolkata)) {
		t.Fatalf("hour window = %v..%v, want 15:00..18:00 local", from, to)
	}
}
