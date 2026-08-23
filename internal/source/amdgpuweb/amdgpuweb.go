// Package amdgpuweb adapts the local amdgputop-web REST API onto the
// domain GPU snapshot. Its DTOs mirror the upstream JSON exactly — every
// metric is nullable and absence is preserved as nil, never guessed.
package amdgpuweb

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// requestTimeout bounds each HTTP call (local daemon).
const requestTimeout = 3 * time.Second

// gpuListing is one entry of GET /api/gpus.
type gpuListing struct {
	ID         string `json:"id"`
	PCI        string `json:"pci"`
	PCIID      string `json:"pci_id"`
	Name       string `json:"name"`
	RenderNode string `json:"render_node"`
}

// metricsEnvelope is GET /api/gpus/{id}/metrics.
type metricsEnvelope struct {
	GPUID   string       `json:"gpu_id"`
	Metrics metricsBlock `json:"metrics"`
}

// metricsBlock mirrors the upstream nullable sensor fields.
type metricsBlock struct {
	GPUBusyPct *float64 `json:"gpu_busy_pct"`
	SCLKMHz    *float64 `json:"sclk_mhz"`
	MCLKMHz    *float64 `json:"mclk_mhz"`
	TempC      *float64 `json:"temp_c"`
	PowerW     *float64 `json:"power_w"`

	VRAMUsedBytes  *uint64 `json:"vram_used_bytes"`
	VRAMTotalBytes *uint64 `json:"vram_total_bytes"`
	GTTUsedBytes   *uint64 `json:"gtt_used_bytes"`
	GTTTotalBytes  *uint64 `json:"gtt_total_bytes"`
}

// Adapter polls one amdgputop-web instance. It is a pointer type: the
// source manager detects configuration changes by pointer identity.
type Adapter struct {
	baseURL    string
	selectedID string
	client     *http.Client
	log        *slog.Logger

	mu         sync.Mutex
	discovered []domain.GPUInfo
}

// New creates an adapter for the given base URL. selectedID may be empty,
// in which case the adapter auto-selects when exactly one GPU exists.
func New(baseURL, selectedID string, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{baseURL: baseURL, selectedID: selectedID, client: client, log: log}
}

// ID implements source.Source.
func (a *Adapter) ID() domain.SourceID { return domain.SourceAMDGPUWeb }

// RequestTimeout implements source.Source.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout }

// Discover lists the GPUs the daemon can see. Results are cached for
// Refresh; Settings uses this directly after a test connection.
func (a *Adapter) Discover(ctx context.Context) ([]domain.GPUInfo, error) {
	var listings []gpuListing
	if err := source.GetJSON(ctx, a.client, a.baseURL+"/api/gpus", nil, &listings); err != nil {
		return nil, err
	}

	gpus := make([]domain.GPUInfo, 0, len(listings))
	for _, l := range listings {
		gpus = append(gpus, domain.GPUInfo{ID: l.ID, Name: l.Name})
	}

	a.mu.Lock()
	a.discovered = gpus
	a.mu.Unlock()
	return gpus, nil
}

// Refresh implements source.Source: resolve the target GPU (explicit
// selection or single-GPU auto-select), fetch its metrics, and map them
// verbatim. A 404 invalidates the discovery cache and retries exactly
// once, so GPU hotplug/config changes are picked up without a restart.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	gpu, err := a.resolveTarget(ctx)
	if err != nil {
		return nil, err
	}

	env, rerr := a.fetchMetrics(ctx, gpu.ID)
	if rerr != nil {
		if rerr.Category == source.CatHTTP && rerr.StatusCode == http.StatusNotFound {
			a.invalidate()
			gpu, err = a.resolveTarget(ctx)
			if err != nil {
				return nil, err
			}
			env, rerr = a.fetchMetrics(ctx, gpu.ID)
		}
		if rerr != nil {
			return nil, rerr
		}
	}

	m := env.Metrics
	return domain.GPUSnapshot{
		CollectedAt: time.Now(),
		DeviceID:    gpu.ID,
		Name:        gpu.Name,

		GPUUtilizationPct: m.GPUBusyPct,
		TemperatureC:      m.TempC,
		PowerWatts:        m.PowerW,
		SCLKMHz:           m.SCLKMHz,
		MCLKMHz:           m.MCLKMHz,

		VRAMUsedBytes:  m.VRAMUsedBytes,
		VRAMTotalBytes: m.VRAMTotalBytes,
		GTTUsedBytes:   m.GTTUsedBytes,
		GTTTotalBytes:  m.GTTTotalBytes,
	}, nil
}

// resolveTarget picks the GPU to poll, re-discovering once when the cache
// is empty or does not contain the selected ID.
func (a *Adapter) resolveTarget(ctx context.Context) (domain.GPUInfo, error) {
	gpus := a.cached()

	if a.selectedID != "" {
		if g := findGPU(gpus, a.selectedID); g.ID != "" {
			return g, nil
		}
		// Cache may be stale; refresh it before giving up.
		var err error
		gpus, err = a.Discover(ctx)
		if err != nil {
			return domain.GPUInfo{}, err
		}
		if g := findGPU(gpus, a.selectedID); g.ID != "" {
			return g, nil
		}
		return domain.GPUInfo{}, &source.Error{
			Category: source.CatConfig,
			Detail:   "Selected GPU not found",
		}
	}

	switch len(gpus) {
	case 0:
		// Empty cache may just mean we never discovered; try once.
		var err error
		gpus, err = a.Discover(ctx)
		if err != nil {
			return domain.GPUInfo{}, err
		}
		if len(gpus) == 1 {
			return gpus[0], nil
		}
		if len(gpus) == 0 {
			return domain.GPUInfo{}, &source.Error{
				Category: source.CatConfig,
				Detail:   "No GPUs discovered",
			}
		}
		return domain.GPUInfo{}, multipleGPUsError(len(gpus))
	case 1:
		return gpus[0], nil
	default:
		return domain.GPUInfo{}, multipleGPUsError(len(gpus))
	}
}

func multipleGPUsError(n int) *source.Error {
	return &source.Error{
		Category: source.CatConfig,
		Detail:   fmt.Sprintf("Multiple GPUs discovered (%d) — select one in Settings", n),
	}
}

func (a *Adapter) fetchMetrics(ctx context.Context, gpuID string) (metricsEnvelope, *source.Error) {
	var env metricsEnvelope
	url := fmt.Sprintf("%s/api/gpus/%s/metrics", a.baseURL, gpuID)
	if err := source.GetJSON(ctx, a.client, url, nil, &env); err != nil {
		return metricsEnvelope{}, err
	}
	return env, nil
}

func (a *Adapter) cached() []domain.GPUInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.GPUInfo(nil), a.discovered...)
}

func (a *Adapter) invalidate() {
	a.mu.Lock()
	a.discovered = nil
	a.mu.Unlock()
}

func findGPU(gpus []domain.GPUInfo, id string) domain.GPUInfo {
	for _, g := range gpus {
		if g.ID == id {
			return g
		}
	}
	return domain.GPUInfo{}
}
