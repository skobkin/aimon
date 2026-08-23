package amdgpuweb

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/skobkin/llm-monitor/internal/domain"
	"github.com/skobkin/llm-monitor/internal/source"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const (
	gpusOne = `[{"id":"amdgpu0","pci":"0000:03:00.0","pci_id":"1002:67df","name":"Radeon RX 580","render_node":"/dev/dri/renderD128"}]`

	gpusTwo = `[
		{"id":"amdgpu0","pci":"0000:03:00.0","pci_id":"1002:67df","name":"Radeon RX 580","render_node":"/dev/dri/renderD128"},
		{"id":"amdgpu1","pci":"0000:08:00.0","pci_id":"1002:73bf","name":"Radeon RX 6700 XT","render_node":"/dev/dri/renderD129"}
	]`

	metricsFull = `{"gpu_id":"amdgpu0","ts":1770000000,"metrics":{
		"gpu_busy_pct":74.5,"mem_busy_pct":31.0,"sclk_mhz":2420,"mclk_mhz":1249,
		"temp_c":63,"fan_rpm":1450,"power_w":172,
		"vram_used_bytes":14800000000,"vram_total_bytes":17179869184,
		"gtt_used_bytes":2254857830,"gtt_total_bytes":68719476736
	}}`

	metricsNulls = `{"gpu_id":"amdgpu0","ts":1770000000,"metrics":{
		"gpu_busy_pct":12.0,"mem_busy_pct":null,"sclk_mhz":null,"mclk_mhz":null,
		"temp_c":null,"fan_rpm":null,"power_w":null,
		"vram_used_bytes":null,"vram_total_bytes":null,
		"gtt_used_bytes":null,"gtt_total_bytes":null
	}}`
)

// serverFixture routes /api/gpus and /api/gpus/{id}/metrics.
type serverFixture struct {
	gpus     atomic.Value // string
	metrics  atomic.Value // string
	gpuCalls atomic.Int64
}

func newFixture(gpus, metrics string) *serverFixture {
	f := &serverFixture{}
	f.gpus.Store(gpus)
	f.metrics.Store(metrics)
	return f
}

func (f *serverFixture) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/gpus":
			f.gpuCalls.Add(1)
			w.Write([]byte(f.gpus.Load().(string)))
		case r.URL.Path == "/api/gpus/amdgpu0/metrics":
			w.Write([]byte(f.metrics.Load().(string)))
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *serverFixture) setMetrics(body string) { f.metrics.Store(body) }

func adapter(url, selected string) *Adapter {
	return New(url, selected, &http.Client{}, testLogger())
}

func asSourceError(t *testing.T, err error) *source.Error {
	t.Helper()
	var serr *source.Error
	if !errors.As(err, &serr) {
		t.Fatalf("expected *source.Error, got %T: %v", err, err)
	}
	return serr
}

func TestRefreshFullMetricsMapping(t *testing.T) {
	f := newFixture(gpusOne, metricsFull)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	a := adapter(srv.URL, "")
	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	gpu := snap.(domain.GPUSnapshot)

	if gpu.DeviceID != "amdgpu0" || gpu.Name != "Radeon RX 580" {
		t.Errorf("identity = %q/%q", gpu.DeviceID, gpu.Name)
	}
	assertFloat(t, "gpu_busy_pct", gpu.GPUUtilizationPct, 74.5)
	assertFloat(t, "temp_c", gpu.TemperatureC, 63)
	assertFloat(t, "power_w", gpu.PowerWatts, 172)
	assertFloat(t, "sclk_mhz", gpu.SCLKMHz, 2420)
	assertFloat(t, "mclk_mhz", gpu.MCLKMHz, 1249)
	if gpu.VRAMUsedBytes == nil || *gpu.VRAMUsedBytes != 14800000000 {
		t.Errorf("vram_used_bytes = %v", gpu.VRAMUsedBytes)
	}
	if gpu.VRAMTotalBytes == nil || *gpu.VRAMTotalBytes != 17179869184 {
		t.Errorf("vram_total_bytes = %v", gpu.VRAMTotalBytes)
	}
	if gpu.GTTUsedBytes == nil || *gpu.GTTUsedBytes != 2254857830 {
		t.Errorf("gtt_used_bytes = %v", gpu.GTTUsedBytes)
	}
	if gpu.GTTTotalBytes == nil || *gpu.GTTTotalBytes != 68719476736 {
		t.Errorf("gtt_total_bytes = %v", gpu.GTTTotalBytes)
	}
}

func TestRefreshNullsStayNil(t *testing.T) {
	f := newFixture(gpusOne, metricsNulls)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	gpu, err := adapter(srv.URL, "").Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	s := gpu.(domain.GPUSnapshot)
	if s.TemperatureC != nil || s.PowerWatts != nil || s.SCLKMHz != nil || s.MCLKMHz != nil {
		t.Error("null metrics must map to nil, not zero")
	}
	if s.VRAMUsedBytes != nil || s.VRAMTotalBytes != nil || s.GTTUsedBytes != nil || s.GTTTotalBytes != nil {
		t.Error("null byte counters must map to nil")
	}
	assertFloat(t, "gpu_busy_pct", s.GPUUtilizationPct, 12.0)
}

func TestAutoSelectSingleGPU(t *testing.T) {
	f := newFixture(gpusOne, metricsFull)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	gpu, err := adapter(srv.URL, "").Refresh(context.Background())
	if err != nil {
		t.Fatalf("single GPU should auto-select: %v", err)
	}
	if got := gpu.(domain.GPUSnapshot).DeviceID; got != "amdgpu0" {
		t.Errorf("auto-selected %q", got)
	}
}

func TestMultipleGPUsRequireSelection(t *testing.T) {
	f := newFixture(gpusTwo, metricsFull)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, err := adapter(srv.URL, "").Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatConfig {
		t.Errorf("category = %s, want config", serr.Category)
	}
	if serr.UserMessage() != "Multiple GPUs discovered (2) — select one in Settings" {
		t.Errorf("message = %q", serr.UserMessage())
	}
}

func TestNoGPUsDiscovered(t *testing.T) {
	f := newFixture(`[]`, metricsFull)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, err := adapter(srv.URL, "").Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatConfig || serr.UserMessage() != "No GPUs discovered" {
		t.Errorf("got %+v", serr)
	}
}

func TestSelectedGPUFoundAfterRediscovery(t *testing.T) {
	f := newFixture(gpusOne, metricsFull)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	a := adapter(srv.URL, "amdgpu0")
	// Prime the cache, then change the discovery result underneath.
	if _, err := a.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.gpus.Store(gpusTwo)
	f.setMetrics(`{"gpu_id":"amdgpu0","metrics":{"gpu_busy_pct":5}}`)

	gpu, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("selection must survive re-discovery: %v", err)
	}
	if got := gpu.(domain.GPUSnapshot).GPUUtilizationPct; got == nil || *got != 5 {
		t.Errorf("expected refreshed metrics, got %v", got)
	}
}

func TestSelectedGPUMissing(t *testing.T) {
	f := newFixture(gpusOne, metricsFull)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	_, err := adapter(srv.URL, "amdgpu9").Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatConfig || serr.UserMessage() != "Selected GPU not found" {
		t.Errorf("got %+v", serr)
	}
}

func TestMetrics404TriggersRediscoveryRetry(t *testing.T) {
	f := newFixture(gpusOne, `{}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/gpus":
			f.gpuCalls.Add(1)
			w.Write([]byte(f.gpus.Load().(string)))
		case r.URL.Path == "/api/gpus/amdgpu0/metrics":
			// 404 until discovery has been re-run, then succeed.
			if f.gpuCalls.Load() >= 2 {
				w.Write([]byte(f.metrics.Load().(string)))
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := adapter(srv.URL, "amdgpu0")
	// Prime cache so the first metrics call 404s with a warm cache.
	if _, err := a.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.setMetrics(`{"gpu_id":"amdgpu0","metrics":{"gpu_busy_pct":9}}`)

	gpu, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("404 must trigger one re-discovery + retry: %v", err)
	}
	if got := gpu.(domain.GPUSnapshot).GPUUtilizationPct; got == nil || *got != 9 {
		t.Errorf("retry metrics = %v", got)
	}
	if f.gpuCalls.Load() < 2 {
		t.Errorf("discovery should have been re-run, calls = %d", f.gpuCalls.Load())
	}
}

func TestSamplerNotReadyIsProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/gpus" {
			w.Write([]byte(gpusOne))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := adapter(srv.URL, "").Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatProvider {
		t.Errorf("503 category = %s", serr.Category)
	}
}

func TestMalformedJSON(t *testing.T) {
	f := newFixture(gpusOne, `{not json`)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	if _, err := adapter(srv.URL, "").Refresh(context.Background()); asSourceError(t, err).Category != source.CatInvalidResponse {
		t.Errorf("malformed category wrong: %v", err)
	}
}

func TestConnectionRefused(t *testing.T) {
	// Nothing listens on port 1.
	if _, err := adapter("http://127.0.0.1:1", "").Refresh(context.Background()); asSourceError(t, err).Category != source.CatConnection {
		t.Errorf("refused category wrong: %v", err)
	}
}

func TestDiscoverReturnsListings(t *testing.T) {
	f := newFixture(gpusTwo, metricsFull)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	gpus, err := adapter(srv.URL, "").Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(gpus) != 2 || gpus[1].Name != "Radeon RX 6700 XT" {
		t.Errorf("discover = %+v", gpus)
	}
}

func TestDiscoveryFailurePropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := adapter(srv.URL, "").Refresh(context.Background()); asSourceError(t, err).Category != source.CatProvider {
		t.Errorf("discovery 500 category wrong: %v", err)
	}
}

func assertFloat(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}
