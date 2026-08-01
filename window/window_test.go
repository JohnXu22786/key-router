package window

import (
	"testing"
	"time"

	"local-router/model"
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
		sw.AddRequest(1) // bucket 0
		testAdvanceTime(sw, 2)    // move to bucket 2
		sw.AddRequest(1)
		sw.AddRequest(1) // bucket 2 gets 2
		testAdvanceTime(sw, 1)    // bucket 3
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

// Helper to simulate time passing by moving lastCleanup backward
func testAdvanceTime(sw *SlidingWindow, n int) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.lastCleanup = sw.lastCleanup.Add(-time.Duration(n) * sw.bucketSize)
}
