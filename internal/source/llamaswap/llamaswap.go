// Package llamaswap adapts the local llama-swap daemon onto the domain
// snapshot. S1 is read-only: only /health and /running are queried; the
// unload/load control APIs are never touched (AGENTS.md).
package llamaswap

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// requestTimeout bounds each HTTP call (local daemon).
const requestTimeout = 3 * time.Second

// runningEnvelope is GET /running. Stopped models are excluded upstream.
type runningEnvelope struct {
	Running []runningEntry `json:"running"`
}

type runningEntry struct {
	Model       string `json:"model"`
	State       string `json:"state"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Adapter polls one llama-swap instance. creds may be nil, meaning the
// instance is known to run without an API key. Pointer type: the manager
// detects configuration changes by pointer identity.
type Adapter struct {
	baseURL string
	creds   credentials.Store
	client  *http.Client
	log     *slog.Logger
}

// New creates an adapter for the given base URL.
func New(baseURL string, creds credentials.Store, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{baseURL: baseURL, creds: creds, client: client, log: log}
}

// Kind implements source.Source.
func (a *Adapter) Kind() domain.SourceKind { return domain.KindLlamaSwap }

// RequestTimeout implements source.Source.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout }

// Refresh implements source.Source: probe /health, then list /running.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}

	header := authHeader(token)

	if _, herr := source.GetText(ctx, a.client, a.baseURL+"/health", header); herr != nil {
		return nil, herr
	}

	var env runningEnvelope
	if rerr := source.GetJSON(ctx, a.client, a.baseURL+"/running", header, &env); rerr != nil {
		return nil, rerr
	}

	models := make([]domain.RunningModel, 0, len(env.Running))
	for _, e := range env.Running {
		models = append(models, domain.RunningModel{ID: e.Model, Name: e.Name})
	}

	return domain.LlamaSwapSnapshot{
		CollectedAt:   time.Now(),
		Healthy:       true,
		RunningModels: models,
	}, nil
}

// Test probes /health only; Settings uses it for connection checks.
func (a *Adapter) Test(ctx context.Context) error {
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	if _, herr := source.GetText(ctx, a.client, a.baseURL+"/health", authHeader(token)); herr != nil {
		return herr
	}
	return nil
}

// token resolves the credential for this refresh. No stored key is a
// normal state (unauthenticated instance); a keychain problem is not.
func (a *Adapter) token(ctx context.Context) (string, error) {
	if a.creds == nil {
		return "", nil
	}
	token, err := a.creds.Get(ctx, credentials.AccountLlamaSwap)
	if err == nil {
		return token, nil
	}
	if errors.Is(err, credentials.ErrNotFound) {
		return "", nil
	}
	var stateErr *credentials.StateError
	if errors.As(err, &stateErr) {
		return "", &source.Error{
			Category: source.CatCredentials,
			Detail:   "Keychain unavailable: " + stateErr.Message,
			Err:      stateErr,
		}
	}
	return "", &source.Error{
		Category: source.CatCredentials,
		Detail:   "Keychain read failed",
		Err:      err,
	}
}

func authHeader(token string) http.Header {
	if token == "" {
		return nil
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}
