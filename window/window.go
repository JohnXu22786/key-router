package window

import (
	"encoding/json"
	"os"
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

	// Backward clock jump (NTP step, manual change, VM suspend): re-anchor
	// instead of freezing — otherwise bucket rotation stops and old counts
	// never expire for the whole jump duration.
	if elapsed < 0 {
		sw.lastCleanup = now
		return
	}

	bucketsToAdvance := int(elapsed / sw.bucketSize)

	if bucketsToAdvance <= 0 {
		return
	}

	// The whole window elapsed (laptop sleep, clock jump, app closed): zero
	// everything and re-anchor lastCleanup to NOW. Without the re-anchor,
	// lastCleanup would lag real time and every subsequent operation would
	// wipe a full window of fresh data (silently disabling the rate limit).
	if bucketsToAdvance >= len(sw.buckets) {
		for i := range sw.buckets {
			sw.buckets[i] = 0
			sw.tokenBuckets[i] = 0
		}
		sw.head = 0
		sw.lastCleanup = now
		return
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

// exportedState is the serializable form of a sliding window
type exportedState struct {
	ReqBuckets  []int64 `json:"req_buckets"`
	TokBuckets  []int64 `json:"tok_buckets"`
	Head        int     `json:"head"`
	LastCleanup int64   `json:"last_cleanup"` // unix nanos
}

// exportState captures the full bucket state (incl. head position and last
// cleanup time) so windows can be restored across restarts
func (sw *SlidingWindow) exportState() exportedState {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	reqCopy := make([]int64, len(sw.buckets))
	tokCopy := make([]int64, len(sw.tokenBuckets))
	copy(reqCopy, sw.buckets)
	copy(tokCopy, sw.tokenBuckets)
	return exportedState{
		ReqBuckets:  reqCopy,
		TokBuckets:  tokCopy,
		Head:        sw.head,
		LastCleanup: sw.lastCleanup.UnixNano(),
	}
}

// importState restores bucket state (from exportState)
func (sw *SlidingWindow) importState(s exportedState) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if len(sw.buckets) == 0 {
		return // avoid divide-by-zero on head modulo below
	}
	if len(s.ReqBuckets) == len(sw.buckets) && len(s.TokBuckets) == len(sw.tokenBuckets) {
		copy(sw.buckets, s.ReqBuckets)
		copy(sw.tokenBuckets, s.TokBuckets)
	}
	sw.head = s.Head % len(sw.buckets)
	if sw.head < 0 {
		sw.head = 0
	}
	if s.LastCleanup <= 0 {
		sw.lastCleanup = time.Now()
	} else {
		sw.lastCleanup = time.Unix(0, s.LastCleanup)
		if sw.lastCleanup.After(time.Now()) {
			sw.lastCleanup = time.Now()
		}
	}
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

// IncrementAll increments request count across all windows, and tokens across all windows
func (wm *WindowManager) IncrementAll(keyID int64, tokens int64) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for _, wt := range []model.WindowType{
		model.WindowRPM, model.WindowTPM, model.WindowRP5h,
		model.WindowRPD, model.WindowRPW, model.WindowRPMo,
	} {
		sw := wm.getOrCreateWindow(keyID, wt)
		sw.AddRequest(1)
		sw.AddTokens(tokens)
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

// Remove drops all window state for a key (e.g. when the key is deleted)
func (wm *WindowManager) Remove(keyID int64) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	delete(wm.windows, keyID)
}

// Prune drops window state for keys not in the given set (e.g. keys deleted
// while the app was closed, whose state would otherwise persist forever)
func (wm *WindowManager) Prune(knownIDs map[int64]bool) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	for keyID := range wm.windows {
		if !knownIDs[keyID] {
			delete(wm.windows, keyID)
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

// PersistedWindows is the serializable form of all window state
type PersistedWindows map[int64]map[model.WindowType]exportedState

// ExportAll captures full bucket state for every key/window so rate-limit
// budgets survive restarts
func (wm *WindowManager) ExportAll() PersistedWindows {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	result := make(PersistedWindows)
	for keyID, km := range wm.windows {
		kmCopy := make(map[model.WindowType]exportedState)
		for wt, sw := range km {
			kmCopy[wt] = sw.exportState()
		}
		result[keyID] = kmCopy
	}
	return result
}

// RestoreAll restores bucket state previously captured by ExportAll
func (wm *WindowManager) RestoreAll(state PersistedWindows) {
	if state == nil {
		return
	}
	wm.mu.Lock()
	defer wm.mu.Unlock()

	for keyID, km := range state {
		keyWindows := wm.windows[keyID]
		if keyWindows == nil {
			keyWindows = make(map[model.WindowType]*SlidingWindow)
			wm.windows[keyID] = keyWindows
		}
		for wt, s := range km {
			// Skip types unknown to the current config (version-skewed or
			// hand-edited files) instead of registering a zero-config window
			// that would panic on advance()/count modulo.
			if _, known := wm.configs[wt]; !known {
				continue
			}
			sw := keyWindows[wt]
			if sw == nil {
				cfg := wm.configs[wt]
				sw = NewSlidingWindowFromConfig(cfg)
				keyWindows[wt] = sw
			}
			sw.importState(s)
		}
	}
}

// SaveToFile persists all window state to a JSON file (atomically via a
// temp file + rename, so a crash mid-write can't corrupt the last state)
func (wm *WindowManager) SaveToFile(path string) error {
	data, err := json.Marshal(wm.ExportAll())
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadFromFile restores window state from a JSON file written by SaveToFile.
// A missing or corrupt file is ignored (fresh state).
func (wm *WindowManager) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state PersistedWindows
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	wm.RestoreAll(state)
	return nil
}
