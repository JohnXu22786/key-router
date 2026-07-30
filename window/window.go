package window

import (
	"sync"
	"time"

	"local-router/model"
)

// SlidingWindow implements a bucket-based sliding window counter
type SlidingWindow struct {
	mu          sync.Mutex
	buckets     []int64
	tokenBuckets []int64
	bucketSize  time.Duration
	head        int
	lastCleanup time.Time
}

// NewSlidingWindow creates a new sliding window counter
// numBuckets: number of time buckets in the window
// bucketSize: duration of each bucket
func NewSlidingWindow(wt model.WindowType, numBuckets int, bucketSize time.Duration) *SlidingWindow {
	now := time.Now()
	return &SlidingWindow{
		buckets:     make([]int64, numBuckets),
		tokenBuckets: make([]int64, numBuckets),
		bucketSize:  bucketSize,
		head:        0,
		lastCleanup: now,
	}
}

// NewSlidingWindowFromConfig creates a new sliding window from a model.WindowConfig
func NewSlidingWindowFromConfig(cfg model.WindowConfig) *SlidingWindow {
	return NewSlidingWindow(cfg.WindowType, cfg.BucketCount, cfg.BucketSize)
}

// advance cleans up expired buckets and advances the head
func (sw *SlidingWindow) advance() {
	now := time.Now()
	elapsed := now.Sub(sw.lastCleanup)
	bucketsToAdvance := int(elapsed / sw.bucketSize)

	if bucketsToAdvance <= 0 {
		return
	}

	// Cap advancement to avoid clearing more than total buckets
	if bucketsToAdvance > len(sw.buckets) {
		bucketsToAdvance = len(sw.buckets)
	}

	for i := 0; i < bucketsToAdvance; i++ {
		sw.head = (sw.head + 1) % len(sw.buckets)
		sw.buckets[sw.head] = 0
		sw.tokenBuckets[sw.head] = 0
	}

	sw.lastCleanup = sw.lastCleanup.Add(time.Duration(bucketsToAdvance) * sw.bucketSize)
}

// AddRequest increments the request count in the current bucket
func (sw *SlidingWindow) AddRequest(count int64) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.advance()
	sw.buckets[sw.head] += count
}

// AddTokens increments the token count in the current bucket
func (sw *SlidingWindow) AddTokens(count int64) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.advance()
	sw.tokenBuckets[sw.head] += count
}

// Count returns the total request count across all buckets
func (sw *SlidingWindow) Count() int64 {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.advance()
	var sum int64
	for _, v := range sw.buckets {
		sum += v
	}
	return sum
}

// TokenCount returns the total token count across all buckets
func (sw *SlidingWindow) TokenCount() int64 {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.advance()
	var sum int64
	for _, v := range sw.tokenBuckets {
		sum += v
	}
	return sum
}

// Reset clears all data in the window
func (sw *SlidingWindow) Reset() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for i := range sw.buckets {
		sw.buckets[i] = 0
		sw.tokenBuckets[i] = 0
	}
	sw.head = 0
	sw.lastCleanup = time.Now()
}

// Snapshot returns a copy of all buckets for persistence
func (sw *SlidingWindow) Snapshot() ([]int64, []int64) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	reqCopy := make([]int64, len(sw.buckets))
	tokCopy := make([]int64, len(sw.tokenBuckets))
	copy(reqCopy, sw.buckets)
	copy(tokCopy, sw.tokenBuckets)
	return reqCopy, tokCopy
}

// WindowManager manages multiple sliding windows per key
type WindowManager struct {
	mu       sync.RWMutex
	windows  map[int64]map[model.WindowType]*SlidingWindow
	configs  map[model.WindowType]model.WindowConfig
}

// NewWindowManager creates a new WindowManager
func NewWindowManager() *WindowManager {
	configs := make(map[model.WindowType]model.WindowConfig)
	for _, cfg := range model.GetWindowConfigs() {
		configs[cfg.WindowType] = cfg
	}
	return &WindowManager{
		windows: make(map[int64]map[model.WindowType]*SlidingWindow),
		configs: configs,
	}
}

// getOrCreateWindow returns or creates a sliding window for a key+type
func (wm *WindowManager) getOrCreateWindow(keyID int64, wt model.WindowType) *SlidingWindow {
	if _, ok := wm.windows[keyID]; !ok {
		wm.windows[keyID] = make(map[model.WindowType]*SlidingWindow)
	}
	if sw, ok := wm.windows[keyID][wt]; ok {
		return sw
	}
	cfg := wm.configs[wt]
	sw := NewSlidingWindowFromConfig(cfg)
	wm.windows[keyID][wt] = sw
	return sw
}

// IncrementRequest adds a request to all windows for a key
func (wm *WindowManager) IncrementRequest(keyID int64, wt model.WindowType) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	sw := wm.getOrCreateWindow(keyID, wt)
	sw.AddRequest(1)
}

// IncrementTokens adds token count to the TPM window for a key
func (wm *WindowManager) IncrementTokens(keyID int64, wt model.WindowType, tokens int64) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	sw := wm.getOrCreateWindow(keyID, wt)
	sw.AddTokens(tokens)
}

// IncrementAll increments request count across all windows, and tokens across TPM
func (wm *WindowManager) IncrementAll(keyID int64, tokens int64) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for _, wt := range []model.WindowType{
		model.WindowRPM, model.WindowTPM, model.WindowRP5h,
		model.WindowRPD, model.WindowRPW, model.WindowRPMo,
	} {
		sw := wm.getOrCreateWindow(keyID, wt)
		sw.AddRequest(1)
		if wt == model.WindowTPM {
			sw.AddTokens(tokens)
		}
	}
}

// GetCount returns the current count for a key+window
func (wm *WindowManager) GetCount(keyID int64, wt model.WindowType) int64 {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	if km, ok := wm.windows[keyID]; ok {
		if sw, ok := km[wt]; ok {
			return sw.Count()
		}
	}
	return 0
}

// GetTokens returns the current token count for a key+window
func (wm *WindowManager) GetTokens(keyID int64, wt model.WindowType) int64 {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	if km, ok := wm.windows[keyID]; ok {
		if sw, ok := km[wt]; ok {
			return sw.TokenCount()
		}
	}
	return 0
}

// IsExceeded checks if a key's window has exceeded the given limit
// limit == 0 means unlimited
func (wm *WindowManager) IsExceeded(keyID int64, wt model.WindowType, limit int64) bool {
	if limit <= 0 {
		return false // unlimited
	}
	count := wm.GetCount(keyID, wt)
	return count >= limit
}

// IsAnyExceeded checks if any window type exceeds the given limits map
// limits may be nil or empty
func (wm *WindowManager) IsAnyExceeded(keyID int64, limits map[model.WindowType]int64) bool {
	for wt, limit := range limits {
		if wm.IsExceeded(keyID, wt, limit) {
			return true
		}
	}
	return false
}

// Reset clears all windows for a key
func (wm *WindowManager) Reset(keyID int64) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if km, ok := wm.windows[keyID]; ok {
		for _, sw := range km {
			sw.Reset()
		}
	}
}

// Snapshot captures all window states
func (wm *WindowManager) Snapshot() map[int64]map[model.WindowType]struct{ Count, TokenCount int64 } {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	result := make(map[int64]map[model.WindowType]struct{ Count, TokenCount int64 })
	for keyID, km := range wm.windows {
		kmCopy := make(map[model.WindowType]struct{ Count, TokenCount int64 })
		for wt, sw := range km {
			kmCopy[wt] = struct{ Count, TokenCount int64 }{
				Count:      sw.Count(),
				TokenCount: sw.TokenCount(),
			}
		}
		result[keyID] = kmCopy
	}
	return result
}
