package domain

import "time"

// GPUInfo is one device from amdgputop-web discovery, used for settings
// and GPU selection.
type GPUInfo struct {
	ID   string
	Name string
}

// GPUSnapshot is a point-in-time reading of one AMD GPU. Every metric is
// optional: amdgputop-web reports null when a sensor is unavailable, and
// absence is preserved as nil rather than guessed (AGENTS.md).
type GPUSnapshot struct {
	CollectedAt time.Time
	DeviceID    string
	Name        string

	GPUUtilizationPct *float64
	TemperatureC      *float64
	PowerWatts        *float64
	SCLKMHz           *float64
	MCLKMHz           *float64

	VRAMUsedBytes  *uint64
	VRAMTotalBytes *uint64
	GTTUsedBytes   *uint64
	GTTTotalBytes  *uint64
}

// SnapshotKind implements Snapshot.
func (s GPUSnapshot) SnapshotKind() SourceKind { return KindAMDGPUWeb }

// Timestamp implements Snapshot.
func (s GPUSnapshot) Timestamp() time.Time { return s.CollectedAt }
