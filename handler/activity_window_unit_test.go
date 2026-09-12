package handler

import (
	"math"
	"testing"
	"time"
)

// TestActivityWindow pins activityWindow's bucket widening for every rollup:
// `from` must be the bucket start containing since (a mid-hour since floors
// to the LOCAL hour), and `to` must normally be the first bucket start AFTER
// until so the bucket containing until is complete. A weekly window crossing
// a month boundary is capped at the next month start. The month branch (year
// rollover) is only exercised here; the hour/day branches are also covered
// through the handler_test suite, and the week branch through
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

	// A month-end week is clipped at the next month boundary instead of
	// widening into the following month's rows.
	monthEnd := time.Date(2026, 9, 30, 23, 59, 59, 0, loc)
	from, to = activityWindow(time.Date(2026, 1, 1, 0, 0, 0, 0, loc), monthEnd, "week")
	if !from.Equal(time.Date(2025, 12, 29, 0, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("week month-end window = %v..%v, want Dec 29..Oct 1", from, to)
	}
	from, to = activityWindow(time.Date(2026, 9, 1, 0, 0, 0, 0, loc), monthEnd, "week")
	if !from.Equal(time.Date(2026, 8, 31, 0, 0, 0, 0, loc)) ||
		!to.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("week month-end window = %v..%v, want Aug 31..Oct 1", from, to)
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

// TestActivityWindowSecondOccurrenceIncludesFollowingBucket verifies that an
// hourly query ending during Pacific/Chatham's misaligned fall-back repeat
// still fetches the row whose persisted 03:00 label starts before the second
// occurrence of 02:45 in epoch time. Resolving the ambiguous 02:00 wall time
// first and then adding an elapsed hour ends at 03:00 and omits that row.
func TestActivityWindowSecondOccurrenceIncludesFollowingBucket(t *testing.T) {
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })

	chatham, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = chatham

	// The second occurrence of 02:45 is the transition instant, represented
	// explicitly with Chatham's post-fallback fixed offset so time.Date cannot
	// choose the first occurrence of the ambiguous wall time.
	until := time.Date(2026, 4, 5, 2, 45, 0, 0, time.FixedZone("CHAST", 12*60*60+45*60)).In(chatham)
	from, to := activityWindow(time.Date(2026, 4, 5, 1, 30, 0, 0, chatham), until, "hour")

	if wantFrom := time.Date(2026, 4, 5, 1, 0, 0, 0, chatham); !from.Equal(wantFrom) {
		t.Fatalf("hour window from = %v, want %v", from, wantFrom)
	}
	if wantTo := time.Date(2026, 4, 5, 4, 0, 0, 0, chatham); !to.Equal(wantTo) {
		t.Fatalf("hour window to = %v, want %v (the 03:00 source row must be fetched)", to, wantTo)
	}
}

// TestActivityWindowSpringForwardDoesNotStall verifies that the hourly query
// end advances past a nonexistent local 02:00 label instead of reconstructing
// the same normalized 01:00 candidate forever. Hourly and total rollups share
// this boundary calculation.
func TestActivityWindowSpringForwardDoesNotStall(t *testing.T) {
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = newYork
	since := time.Date(2026, 3, 8, 0, 30, 0, 0, newYork)
	until := time.Date(2026, 3, 8, 1, 30, 0, 0, newYork)
	wantFrom := time.Date(2026, 3, 8, 0, 0, 0, 0, newYork)
	wantTo := time.Date(2026, 3, 8, 3, 0, 0, 0, newYork)
	for _, rollup := range []string{"hour", "total"} {
		from, to := activityWindow(since, until, rollup)
		if !from.Equal(wantFrom) || !to.Equal(wantTo) {
			t.Fatalf("%s window = %v..%v, want %v..%v", rollup, from, to, wantFrom, wantTo)
		}
	}
}

// TestActivityRowWindowShareDSTReconstructsFixedOffsetBucket verifies the
// precise Activity path against the way SQLite serializes HourBucket values:
// the loaded time has a fixed offset even though RecordConsumption merged the
// repeated local hour using time.Local. The row's denominator must therefore
// include both passes of the local hour.
func TestActivityRowWindowShareDSTReconstructsFixedOffsetBucket(t *testing.T) {
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })

	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = ny
	firstNY := time.Date(2026, 11, 1, 1, 0, 0, 0, time.FixedZone("EDT", -4*60*60))
	shareNY := activityRowWindowShare(
		firstNY,
		firstNY.Add(30*time.Minute),
		firstNY.Add(2*time.Hour),
		firstNY.Add(3*time.Hour),
	)
	if math.Abs(shareNY-0.75) > 1e-9 {
		t.Fatalf("New York repeated-hour share = %v, want 0.75", shareNY)
	}

	lordHowe, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = lordHowe
	firstLordHowe := time.Date(2026, 4, 5, 1, 0, 0, 0, time.FixedZone("LHDT", 11*60*60))
	shareLordHowe := activityRowWindowShare(
		firstLordHowe,
		firstLordHowe.Add(30*time.Minute),
		firstLordHowe.Add(90*time.Minute),
		firstLordHowe.Add(3*time.Hour),
	)
	if math.Abs(shareLordHowe-(2.0/3.0)) > 1e-9 {
		t.Fatalf("Lord Howe repeated-hour share = %v, want 2/3", shareLordHowe)
	}
}

// TestActivitySpringForwardNormalizedBuckets verifies the timestamps that
// RecordConsumption persists when the requested local hour begins inside a
// spring-forward gap. time.Date normalizes Lord Howe's 02:00 to 02:30 and
// Chatham's 03:00 to 04:00; both rows must still retain their real epoch
// coverage for precise windows and query widening.
func TestActivitySpringForwardNormalizedBuckets(t *testing.T) {
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })

	lordHowe, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = lordHowe
	normalizedLordHowe := time.Date(2026, 10, 4, 2, 0, 0, 0, lordHowe)
	if normalizedLordHowe.Hour() != 2 || normalizedLordHowe.Minute() != 30 {
		t.Fatalf("Lord Howe normalized bucket = %v, want 02:30", normalizedLordHowe)
	}
	// GORM/SQLite returns the persisted timestamp with a fixed offset rather
	// than the IANA location used while recording it.
	persistedLordHowe := normalizedLordHowe.In(time.FixedZone("LHDT", 11*60*60))
	lordHoweRuns := activityHourRunsInLocation(persistedLordHowe, lordHowe)
	if len(lordHoweRuns) != 1 ||
		!lordHoweRuns[0].from.Equal(time.Date(2026, 10, 4, 2, 30, 0, 0, lordHowe)) ||
		!lordHoweRuns[0].to.Equal(time.Date(2026, 10, 4, 3, 0, 0, 0, lordHowe)) {
		t.Fatalf("Lord Howe runs = %+v, want 02:30..03:00", lordHoweRuns)
	}
	lordHoweShare := activityRowWindowShare(
		persistedLordHowe,
		time.Date(2026, 10, 4, 2, 45, 0, 0, lordHowe),
		time.Date(2026, 10, 4, 3, 0, 0, 0, lordHowe),
		time.Date(2026, 10, 4, 4, 0, 0, 0, lordHowe),
	)
	if math.Abs(lordHoweShare-0.5) > 1e-9 {
		t.Fatalf("Lord Howe spring-forward share = %v, want 0.5", lordHoweShare)
	}

	chatham, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = chatham
	normalizedChatham := time.Date(2026, 9, 27, 3, 0, 0, 0, chatham)
	if normalizedChatham.Hour() != 4 || normalizedChatham.Minute() != 0 {
		t.Fatalf("Chatham normalized bucket = %v, want 04:00", normalizedChatham)
	}
	persistedChatham := normalizedChatham.In(time.FixedZone("CHADT", 13*60*60+45*60))
	chathamRuns := activityHourRunsInLocation(persistedChatham, chatham)
	if len(chathamRuns) != 1 ||
		!chathamRuns[0].from.Equal(time.Date(2026, 9, 27, 3, 45, 0, 0, chatham)) ||
		!chathamRuns[0].to.Equal(time.Date(2026, 9, 27, 5, 0, 0, 0, chatham)) {
		t.Fatalf("Chatham runs = %+v, want 03:45..05:00", chathamRuns)
	}
	chathamShare := activityRowWindowShare(
		persistedChatham,
		time.Date(2026, 9, 27, 3, 45, 0, 0, chatham),
		time.Date(2026, 9, 27, 4, 0, 0, 0, chatham),
		time.Date(2026, 9, 27, 6, 0, 0, 0, chatham),
	)
	if math.Abs(chathamShare-0.2) > 1e-9 {
		t.Fatalf("Chatham spring-forward share = %v, want 0.2", chathamShare)
	}

	// The 03:45–04:00 portion is stored under the 04:00 key. The query end
	// must therefore pass the following 05:00 label so that SQL includes it.
	_, to := activityWindow(
		time.Date(2026, 9, 27, 3, 45, 0, 0, chatham),
		time.Date(2026, 9, 27, 3, 50, 0, 0, chatham),
		"hour",
	)
	if want := time.Date(2026, 9, 27, 5, 0, 0, 0, chatham); !to.Equal(want) {
		t.Fatalf("Chatham spring-forward query end = %v, want %v", to, want)
	}
}
