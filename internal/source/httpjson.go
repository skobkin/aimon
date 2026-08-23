package source

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// maxBodyBytes caps how much of a response is read before unmarshalling.
// Provider payloads are tiny; anything larger is treated as invalid.
const maxBodyBytes = 1 << 20

// GetJSON performs a GET with the supplied headers and decodes the JSON
// body into dst. Every failure is returned as a classified *Error;
// adapters never hand-roll requests. Bodies are never included in errors.
func GetJSON(ctx context.Context, client *http.Client, url string, header http.Header, dst any) *Error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &Error{Category: CatConfig, Detail: "invalid request URL", Err: err}
	}
	for k, vs := range header {
		req.Header[k] = vs
	}

	resp, err := client.Do(req)
	if err != nil {
		return transportError("request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return &Error{Category: CatConnection, Detail: "reading response body", Err: err}
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return &Error{Category: CatInvalidResponse, Detail: "malformed JSON", Err: err}
	}
	return nil
}

// GetText performs a GET and returns the raw body, used for the
// llama-swap plain-text /health probe.
func GetText(ctx context.Context, client *http.Client, url string, header http.Header) (string, *Error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", &Error{Category: CatConfig, Detail: "invalid request URL", Err: err}
	}
	for k, vs := range header {
		req.Header[k] = vs
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", transportError("request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", statusError(resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", &Error{Category: CatConnection, Detail: "reading response body", Err: err}
	}
	return string(body), nil
}
