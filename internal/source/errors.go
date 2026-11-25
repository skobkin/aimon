// Package source provides the source manager that runs independent
// per-source polling loops, plus the shared error model and HTTP helper
// used by all adapters. Provider DTOs stay inside adapter subpackages.
package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// Category classifies a refresh failure for the UI. Categories map onto
// fixed user-facing strings; details stay in logs (NFR-3).
type Category string

// Refresh failure categories.
const (
	CatConnection      Category = "connection"
	CatTimeout         Category = "timeout"
	CatAuth            Category = "auth"
	CatRateLimited     Category = "rate-limited"
	CatInvalidResponse Category = "invalid-response"
	CatProvider        Category = "provider"
	// CatBackend is a rejection from a non-HTTP surface (currently a
	// JSON-RPC error object): no HTTP status exists, so the HTTP-specific
	// provider wording must not be used for it.
	CatBackend     Category = "backend"
	CatHTTP        Category = "http"
	CatCredentials Category = "credentials"
	CatConfig      Category = "config"
)

// Error is a classified refresh failure. Detail is internal context for
// logs (never bodies, tokens, or provider status messages); UserMessage is
// what the UI shows.
type Error struct {
	Category   Category
	Detail     string
	StatusCode int // set for CatHTTP/CatAuth/CatRateLimited/CatProvider
	Err        error
	// Message is an optional provider-authored explanation that the adapter
	// has already run through SanitizeProviderMessage. UserMessage appends
	// it to the category's fixed text. Only adapters that opt in set it
	// (docs/DECISION_LOG.md, 2026-11-25); everywhere else the UI text stays
	// fixed per category.
	Message string
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Category, e.Detail, e.Err)
	}

	return fmt.Sprintf("%s: %s", e.Category, e.Detail)
}

func (e *Error) Unwrap() error { return e.Err }

// UserMessage returns the fixed UI string for the category. Credentials
// and Config details are authored to be user-appropriate and pass through.
func (e *Error) UserMessage() string {
	msg := e.categoryMessage()
	if e.Message == "" || e.Category == CatCredentials || e.Category == CatConfig {
		return msg
	}

	return msg + ": " + e.Message
}

func (e *Error) categoryMessage() string {
	switch e.Category {
	case CatConnection:
		return "Connection failed"
	case CatTimeout:
		return "Request timed out"
	case CatAuth:
		return "Authentication failed"
	case CatRateLimited:
		return "Rate limited"
	case CatInvalidResponse:
		return "Invalid provider response"
	case CatProvider:
		return "Provider error (HTTP 5xx)"
	case CatBackend:
		return "Provider rejected the request"
	case CatHTTP:
		return fmt.Sprintf("Provider returned HTTP %d", e.StatusCode)
	case CatCredentials, CatConfig:
		return e.Detail
	}

	return e.Detail
}

// MaxProviderMessageRunes caps a provider message shown in the UI.
const MaxProviderMessageRunes = 80

// SanitizeProviderMessage turns provider-authored text into something safe
// to show: whitespace collapsed, non-printable runes dropped, capped at
// MaxProviderMessageRunes. It returns "" — show nothing — when the text
// contains any of the given secrets (an API key echoed back by the
// provider must never reach the UI).
func SanitizeProviderMessage(msg string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" && strings.Contains(msg, sec) {
			return ""
		}
	}
	clean := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}

		return r
	}, msg)
	clean = strings.Join(strings.Fields(clean), " ")
	if r := []rune(clean); len(r) > MaxProviderMessageRunes {
		clean = string(r[:MaxProviderMessageRunes-1]) + "…"
	}

	return clean
}

// classifyTransport maps a transport-level error onto a Category.
// Timeouts (including context deadlines) read as timeouts; everything
// else is a connection failure.
func classifyTransport(err error) Category {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return CatTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CatTimeout
	}

	return CatConnection
}

// classifyStatus maps a non-2xx HTTP status onto a Category.
func classifyStatus(code int) Category {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return CatAuth
	case code == http.StatusTooManyRequests:
		return CatRateLimited
	case code >= 500:
		return CatProvider
	}

	return CatHTTP
}

// statusError builds a classified *Error for a non-2xx status.
func statusError(code int) *Error {
	return &Error{
		Category:   classifyStatus(code),
		Detail:     fmt.Sprintf("HTTP %d", code),
		StatusCode: code,
	}
}

// transportError builds a classified *Error for a failed request.
func transportError(op string, err error) *Error {
	return &Error{
		Category: classifyTransport(err),
		Detail:   op,
		Err:      unwrapURLError(err),
	}
}

func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}

	return err
}
