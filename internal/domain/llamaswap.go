package domain

import "time"

// RunningModel is one model process currently tracked by llama-swap.
// Activity counters (requests active/waiting) are deliberately absent:
// llama-swap exposes no request metrics, and S1 does not invent data.
type RunningModel struct {
	ID   string
	Name string
}

// LlamaSwapSnapshot is the merged /health + /running view of llama-swap.
type LlamaSwapSnapshot struct {
	CollectedAt   time.Time
	Healthy       bool
	RunningModels []RunningModel
}

// SnapshotSource implements Snapshot.
func (s LlamaSwapSnapshot) SnapshotSource() SourceID { return SourceLlamaSwap }

// Timestamp implements Snapshot.
func (s LlamaSwapSnapshot) Timestamp() time.Time { return s.CollectedAt }
