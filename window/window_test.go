package window

import (
	"testing"
	"time"

	"key-router/model"
)

func TestSlidingWindow_AddRequest(t *testing.T) {
	t.Run("increments request count", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPM, 10, time.Second)
		sw.AddRequest(1)
		sw.AddRequest(1)
		if got := sw.Count(); got != 2 {
			t.Errorf("Count() = %d, want 2", got)
		}
	})
}

func TestSlidingWindow_AddTokens(t *testing.T) {
	t.Run("increments token count", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowTPM, 10, time.Second)
		sw.AddTokens(150)
		sw.AddTokens(250)
		if got := sw.TokenCount(); got != 400 {
			t.Errorf("TokenCount() = %d, want 400", got)
		}
	})
}

func TestSlidingWindow_Count(t *testing.T) {
	t.Run("returns sum of all buckets", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPM, 5, time.Second)
		// Add at different times
		sw.AddRequest(1)       // bucket 0
		testAdvanceTime(sw, 2) // move to bucket 2
		sw.AddRequest(1)
		sw.AddRequest(1)       // bucket 2 gets 2
		testAdvanceTime(sw, 1) // bucket 3
		sw.AddRequest(1)

		if got := sw.Count(); got != 4 {
			t.Errorf("Count() = %d, want 4 (1+2+1)", got)
		}
	})
}

func TestSlidingWindow_AutoCleanup(t *testing.T) {
	t.Run("expired buckets are zeroed", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPM, 5, time.Second)
		sw.AddRequest(5) // fill bucket 0
		// advance past all buckets
		testAdvanceTime(sw, 10)
		if got := sw.Count(); got != 0 {
			t.Errorf("Count() after full rotation = %d, want 0", got)
		}
	})
}

func TestSlidingWindow_OldBucketsAreZero(t *testing.T) {
	t.Run("old data naturally slides out", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPM, 5, time.Second)

		t0Count := sw.Count()
		// Advance 1 bucket
		testAdvanceTime(sw, 1)
		sw.AddRequest(1)

		t1Count := sw.Count()
		// Advance past all buckets - should be 0
		testAdvanceTime(sw, 5)
		t5Count := sw.Count()

		if t0Count != 0 {
			t.Errorf("initial count should be 0, got %d", t0Count)
		}
		if t1Count != 1 {
			t.Errorf("count after 1 request should be 1, got %d", t1Count)
		}
		if t5Count != 0 {
			t.Errorf("count after full window slide should be 0, got %d", t5Count)
		}
	})
}

// TestSlidingWindow_CostExpiry guards the cost-metric rate limit windows:
// cost buckets must expire exactly like request/token buckets when the head
// rotates. Regression: advance() used to zero only buckets and tokenBuckets,
// so cost-metric windows (5h/daily/weekly/monthly) never decayed and every
// window showed the lifetime total cost.
func TestSlidingWindow_CostExpiry(t *testing.T) {
	t.Run("full rotation zeroes cost", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRP5h, 60, 5*time.Minute)
		sw.AddCost(2_000_000) // $2 in bucket 0
		// advance past all buckets
		testAdvanceTime(sw, 60)
		if got := sw.CostCount(); got != 0 {
			t.Errorf("CostCount() after full rotation = %d, want 0", got)
		}
	})

	t.Run("partial rotation expires only old cost", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPM, 10, time.Second)
		sw.AddCost(100) // bucket 0
		testAdvanceTime(sw, 5)
		sw.AddCost(50) // bucket 5
		// advance 5 more: bucket 0 (100) slides out, bucket 5 (50) stays
		testAdvanceTime(sw, 5)
		if got := sw.CostCount(); got != 50 {
			t.Errorf("CostCount() after partial rotation = %d, want 50", got)
		}
	})
}

func TestWindowManager_AllWindows(t *testing.T) {
	t.Run("can create and use all window types", func(t *testing.T) {
		wm := NewWindowManager()
		keyID := int64(1)

		for _, wt := range []model.WindowType{
			model.WindowRPM, model.WindowTPM, model.WindowRP5h,
			model.WindowRPD, model.WindowRPW, model.WindowRPMo,
		} {
			t.Run(string(wt), func(t *testing.T) {
				wm.IncrementRequest(keyID, wt)
				wm.IncrementTokens(keyID, wt, 100)

				count := wm.GetCount(keyID, wt)
				tokens := wm.GetTokens(keyID, wt)

				if count != 1 {
					t.Errorf("Request count for %s = %d, want 1", wt, count)
				}
				if tokens != 100 {
					t.Errorf("Token count for %s = %d, want 100", wt, tokens)
				}
			})
		}
	})
}

func TestWindowManager_Reset(t *testing.T) {
	t.Run("reset clears all data", func(t *testing.T) {
		wm := NewWindowManager()
		keyID := int64(1)

		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementTokens(keyID, model.WindowTPM, 500)

		wm.Reset(keyID)

		if count := wm.GetCount(keyID, model.WindowRPM); count != 0 {
			t.Errorf("after reset Count() = %d, want 0", count)
		}
		if tokens := wm.GetTokens(keyID, model.WindowTPM); tokens != 0 {
			t.Errorf("after reset TokenCount() = %d, want 0", tokens)
		}
	})
}

func TestWindowManager_Snapshot(t *testing.T) {
	t.Run("snapshot captures current state", func(t *testing.T) {
		wm := NewWindowManager()
		keyID := int64(1)

		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPD)

		state := wm.Snapshot()
		if _, ok := state[keyID]; !ok {
			t.Fatal("key not found in snapshot")
		}
		if rpmCount := state[keyID][model.WindowRPM].Count; rpmCount != 2 {
			t.Errorf("RPM count = %d, want 2", rpmCount)
		}
	})
}

func TestWindowManager_ExportRestore(t *testing.T) {
	t.Run("export/restore round-trip preserves counts", func(t *testing.T) {
		wm := NewWindowManager()
		keyID := int64(1)

		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPD)
		wm.IncrementTokens(keyID, model.WindowTPM, 150)

		state := wm.ExportAll()

		// Fresh manager, restore from state
		wm2 := NewWindowManager()
		wm2.RestoreAll(state)

		if got := wm2.GetCount(keyID, model.WindowRPM); got != 2 {
			t.Errorf("restored RPM count = %d, want 2", got)
		}
		if got := wm2.GetCount(keyID, model.WindowRPD); got != 1 {
			t.Errorf("restored RPD count = %d, want 1", got)
		}
		if got := wm2.GetTokens(keyID, model.WindowTPM); got != 150 {
			t.Errorf("restored TPM tokens = %d, want 150", got)
		}

		// Nil state is a no-op
		wm2.RestoreAll(nil)
		if got := wm2.GetCount(keyID, model.WindowRPM); got != 2 {
			t.Errorf("count changed after nil restore = %d, want 2", got)
		}
	})
}

func TestWindowManager_CheckLimit(t *testing.T) {
	t.Run("respects RPM limit", func(t *testing.T) {
		wm := NewWindowManager()
		keyID := int64(1)

		// Not exceeded
		wm.IncrementRequest(keyID, model.WindowRPM)
		if exceeded := wm.IsExceeded(keyID, model.WindowRPM, 10); exceeded {
			t.Error("expected within limit")
		}

		// Exceeded
		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPM)
		wm.IncrementRequest(keyID, model.WindowRPM)

		if exceeded := wm.IsExceeded(keyID, model.WindowRPM, 5); !exceeded {
			t.Error("expected limit exceeded")
		}
	})

	t.Run("limit 0 means unlimited", func(t *testing.T) {
		wm := NewWindowManager()
		keyID := int64(1)

		for i := 0; i < 100; i++ {
			wm.IncrementRequest(keyID, model.WindowRPM)
		}

		if exceeded := wm.IsExceeded(keyID, model.WindowRPM, 0); exceeded {
			t.Error("limit 0 should be unlimited")
		}
	})
}

// TestSlidingWindow_RestoredOldStateDecays guards the "old data residue"
// scenario: a window restored from persisted state (windows.json) whose
// lastCleanup is far in the past must slide the stale buckets out on the
// first access — old counts expire exactly on schedule, never stick forever.
func TestSlidingWindow_RestoredOldStateDecays(t *testing.T) {
	// Mirrors the real persisted weekly window (key 6 in windows.json):
	// 3 days of usage, head=2 (bucket 2 is the current bucket, spanning
	// [lastCleanup, lastCleanup+24h)), buckets 0-1 hold the two earlier
	// days.
	persisted := func(lastCleanup time.Time) exportedState {
		return exportedState{
			ReqBuckets:  []int64{660, 7522, 1034, 0, 0, 0, 0},
			TokBuckets:  []int64{0, 0, 0, 0, 0, 0, 0},
			CostBuckets: []int64{0, 0, 0, 0, 0, 0, 0},
			Head:        2,
			LastCleanup: lastCleanup.UnixNano(),
		}
	}
	// The real file saves mid-bucket, so lastCleanup is anchored 5h into
	// the bucket that was current when the app closed: elapsed at access
	// is closure + 5h, keeping every boundary below ≥5h from truncation.
	closed := func(days int) time.Time {
		return time.Now().Add(-time.Duration(days)*24*time.Hour - 5*time.Hour)
	}

	t.Run("3-day closure keeps all data (still within window)", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPW, 7, 24*time.Hour)
		sw.importState(persisted(closed(3)))
		if got := sw.Count(); got != 9216 {
			t.Errorf("Count() after 3-day closure = %d, want 9216", got)
		}
	})

	t.Run("6-day closure drops buckets whose start passed the window edge", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPW, 7, 24*time.Hour)
		sw.importState(persisted(closed(6)))
		// Buckets 0 and 1 started 8d5h / 7d5h before reopen — past the
		// 7-day edge, so they slide out. Only the current bucket (started
		// 6d5h before reopen) survives: the weekly window quantizes to
		// 24h buckets, dropping a bucket when its START passes the edge.
		if got := sw.Count(); got != 1034 {
			t.Errorf("Count() after 6-day closure = %d, want 1034", got)
		}
	})

	t.Run("closure past the whole window zeroes everything", func(t *testing.T) {
		sw := NewSlidingWindow(model.WindowRPW, 7, 24*time.Hour)
		sw.importState(persisted(closed(8)))
		if got := sw.Count(); got != 0 {
			t.Errorf("Count() after 8-day closure = %d, want 0", got)
		}
	})

	t.Run("manager restore then closure slides like the live app", func(t *testing.T) {
		wm := NewWindowManager()
		wm.RestoreAll(PersistedWindows{
			6: {
				model.WindowRPW: persisted(closed(3)),
			},
		})
		if got := wm.GetCount(6, model.WindowRPW); got != 9216 {
			t.Errorf("GetCount after restore+3-day closure = %d, want 9216", got)
		}
	})
}

// Helper to simulate time passing by moving lastCleanup backward
func testAdvanceTime(sw *SlidingWindow, n int) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.lastCleanup = sw.lastCleanup.Add(-time.Duration(n) * sw.bucketSize)
}
