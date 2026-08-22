package bootstrap

import (
	"bytes"
	"io"
	"sync"
	"time"
)

const (
	// obsidian-headless normally prints "Fully synced" every 30 seconds.
	// Two minutes tolerates several missed heartbeats before traffic is removed.
	syncReadyMaxAge = 2 * time.Minute
	// A process that stays alive but produces no heartbeat for five minutes is
	// wedged rather than merely reconnecting; restart just that sync child.
	defaultWatchdogAfter = 5 * time.Minute
	defaultWatchdogPoll  = 15 * time.Second
)

type syncState struct {
	running       bool
	startedAt     time.Time
	lastHeartbeat time.Time
}

// SyncReady reports whether every configured vault has a running sync child
// and has produced a recent "Fully synced" heartbeat.
func (b *Bootstrapper) SyncReady() bool {
	b.healthMu.RLock()
	defer b.healthMu.RUnlock()
	if len(b.cfg.Vaults) == 0 {
		return false
	}
	now := b.now()
	for _, vault := range b.cfg.Vaults {
		state := b.syncStates[vault.Name]
		if state == nil || !state.running || state.lastHeartbeat.IsZero() ||
			now.Sub(state.lastHeartbeat) > syncReadyMaxAge {
			return false
		}
	}
	return true
}

func (b *Bootstrapper) syncStarted(vault string) {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()
	b.syncStates[vault] = &syncState{running: true, startedAt: b.now()}
}

func (b *Bootstrapper) syncHeartbeat(vault string) {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()
	state := b.syncStates[vault]
	if state == nil {
		state = &syncState{running: true, startedAt: b.now()}
		b.syncStates[vault] = state
	}
	state.lastHeartbeat = b.now()
}

func (b *Bootstrapper) syncStopped(vault string) {
	b.healthMu.Lock()
	defer b.healthMu.Unlock()
	if state := b.syncStates[vault]; state != nil {
		state.running = false
	}
}

func (b *Bootstrapper) syncStale(vault string, maxAge time.Duration) bool {
	b.healthMu.RLock()
	defer b.healthMu.RUnlock()
	state := b.syncStates[vault]
	if state == nil || !state.running {
		return true
	}
	last := state.lastHeartbeat
	if last.IsZero() {
		last = state.startedAt
	}
	return b.now().Sub(last) > maxAge
}

// observedSyncWriter forwards ob output unchanged while recognizing complete
// heartbeat lines even when writes split a line across multiple chunks.
type observedSyncWriter struct {
	b     *Bootstrapper
	vault string

	mu      sync.Mutex
	pending []byte
}

func (b *Bootstrapper) observedSyncOutput(vault string) io.Writer {
	return &observedSyncWriter{b: b, vault: vault}
}

func (w *observedSyncWriter) Write(p []byte) (int, error) {
	w.b.outputMu.Lock()
	n, err := w.b.syncOutput.Write(p)
	w.b.outputMu.Unlock()

	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p[:n]...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		line := bytes.TrimSuffix(w.pending[:newline], []byte{'\r'})
		if bytes.Equal(line, []byte("Fully synced")) {
			w.b.syncHeartbeat(w.vault)
		}
		w.pending = w.pending[newline+1:]
	}
	return n, err
}
