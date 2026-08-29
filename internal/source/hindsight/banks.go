package hindsight

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// bankListResponse models ONLY the aggregate fields of the paginated bank
// list. The upstream items carry bank identity/profile fields (IDs, names,
// missions) that S5 deliberately does not map: what is not in the struct
// cannot leak into snapshots, logs, or the UI (docs/S5_AGENT_RULES.md §2).
type bankListResponse struct {
	Banks  []bankAggregateItem `json:"banks"`
	Total  int64               `json:"total"`
	Limit  int64               `json:"limit"`
	Offset int64               `json:"offset"`
}

// bankAggregateItem keeps the three fields aggregation needs. fact_count
// is optional upstream and defaults to 0; the timestamps are optional.
type bankAggregateItem struct {
	FactCount      *int64     `json:"fact_count"`
	LastDocumentAt *time.Time `json:"last_document_at"`
	LastWriteAt    *time.Time `json:"last_write_at"`
}

// bankAggregates is the deployment-wide result of walking the bank list.
type bankAggregates struct {
	// Count is the response's total bank count — never a row count, so a
	// truncated walk cannot silently under-report.
	Count          int64
	Facts          int64
	LastWriteAt    *time.Time
	LastDocumentAt *time.Time
}

// pageResult is one fetched page: its aggregate contribution plus the row
// count the pagination walk advances by.
type pageResult struct {
	agg  bankAggregates
	rows int64
}

// maxBankPages bounds the pagination walk. With the 1000-row page size
// this allows ten million banks — far past any personal deployment —
// while a broken server cannot keep the adapter requesting forever.
const maxBankPages = 10000

// fetchBanks walks GET /v1/default/banks?limit=N&offset=M until the
// response's own total is covered. The echoed limit is never trusted for
// advancement (the server may cap the page size); progress is measured by
// returned rows. Only list-page requests are ever made — never one
// request per bank.
func (a *Adapter) fetchBanks(ctx context.Context, header http.Header) (bankAggregates, error) {
	var agg bankAggregates
	var offset int64

	for page := 0; ; page++ {
		if page >= maxBankPages {
			return bankAggregates{}, &source.Error{
				Category: source.CatInvalidResponse,
				Detail:   "bank pagination exceeded the page budget",
			}
		}
		res, err := a.fetchBanksPage(ctx, header, int64(a.pageSize), offset)
		if err != nil {
			return bankAggregates{}, err
		}
		if page == 0 {
			if res.agg.Count < 0 {
				return bankAggregates{}, &source.Error{
					Category: source.CatInvalidResponse,
					Detail:   "bank list total is negative",
				}
			}
			agg.Count = res.agg.Count
		}
		// Same overflow guard as the within-page accumulation: each page's
		// sum fits, but the deployment-wide total can still overflow.
		next := agg.Facts + res.agg.Facts
		if next < agg.Facts {
			return bankAggregates{}, &source.Error{
				Category: source.CatInvalidResponse,
				Detail:   "bank fact counts overflow",
			}
		}
		agg.Facts = next
		agg.LastWriteAt = maxTime(agg.LastWriteAt, res.agg.LastWriteAt)
		agg.LastDocumentAt = maxTime(agg.LastDocumentAt, res.agg.LastDocumentAt)

		// Advancement follows the rows the server actually returned, not
		// the requested or echoed limit.
		if res.rows <= 0 {
			if offset < agg.Count {
				return bankAggregates{}, &source.Error{
					Category: source.CatInvalidResponse,
					Detail:   "bank list ended before reaching its total",
				}
			}
			return agg, nil
		}
		offset += res.rows
		if offset >= agg.Count {
			return agg, nil
		}
	}
}

// fetchBanksPage fetches one page and aggregates it.
func (a *Adapter) fetchBanksPage(ctx context.Context, header http.Header, limit, offset int64) (pageResult, error) {
	if limit <= 0 || offset < 0 {
		return pageResult{}, &source.Error{
			Category: source.CatConfig,
			Detail:   "invalid bank page request",
		}
	}
	var resp bankListResponse
	url := bankPageURL(a.baseURL, limit, offset)
	if err := source.GetJSON(ctx, a.client, url, header, &resp); err != nil {
		return pageResult{}, err
	}
	if resp.Total < 0 || resp.Offset < 0 {
		return pageResult{}, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "bank list pagination fields are negative",
		}
	}

	res := pageResult{rows: int64(len(resp.Banks))}
	res.agg.Count = resp.Total
	for i := range resp.Banks {
		item := &resp.Banks[i]
		if item.FactCount != nil {
			if *item.FactCount < 0 {
				return pageResult{}, &source.Error{
					Category: source.CatInvalidResponse,
					Detail:   "bank fact count is negative",
				}
			}
			next := res.agg.Facts + *item.FactCount
			if next < res.agg.Facts {
				return pageResult{}, &source.Error{
					Category: source.CatInvalidResponse,
					Detail:   "bank fact counts overflow",
				}
			}
			res.agg.Facts = next
		}
		res.agg.LastWriteAt = maxTime(res.agg.LastWriteAt, item.LastWriteAt)
		res.agg.LastDocumentAt = maxTime(res.agg.LastDocumentAt, item.LastDocumentAt)
	}
	return res, nil
}

// bankPageURL builds one paginated list URL: /v1/default/banks with fixed
// limit/offset query parameters.
func bankPageURL(base string, limit, offset int64) string {
	return base + "/v1/default/banks?limit=" + strconv.FormatInt(limit, 10) +
		"&offset=" + strconv.FormatInt(offset, 10)
}

// maxTime returns the later of two optional instants; nil loses.
func maxTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	default:
		return a
	}
}
