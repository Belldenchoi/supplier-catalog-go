package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrInterrupted     = errors.New("update interrupted; resume this run to continue")
	ErrAttention       = errors.New("all batches processed, but unresolved issues need attention")
	errProgressChanged = errors.New("run progress changed")
	identifier         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	currencyCode       = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Record is a full replacement at a monotonically increasing per-SKU version.
// A withdrawal is a tombstone; no product details are required.
type Record struct {
	SKU        string `json:"sku"`
	Version    int64  `json:"version"`
	Name       string `json:"name,omitempty"`
	PriceCents *int64 `json:"price_cents,omitempty"`
	Currency   string `json:"currency,omitempty"`
	Withdrawn  bool   `json:"withdrawn"`
}

type Product struct {
	Record
	SupplierID string `json:"supplier_id"`
	LastRunID  string `json:"last_run_id"`
}

type Page struct {
	SupplierID string            `json:"supplier_id"`
	UpdateID   string            `json:"update_id"`
	Cursor     string            `json:"cursor"`
	NextCursor *string           `json:"next_cursor"`
	Records    []json.RawMessage `json:"records"`
}

type Counts struct {
	Pages      int `json:"pages"`
	Records    int `json:"records"`
	Added      int `json:"added"`
	Updated    int `json:"updated"`
	Withdrawn  int `json:"withdrawn"`
	Restored   int `json:"restored"`
	Unchanged  int `json:"unchanged"`
	Duplicates int `json:"duplicates"`
	Stale      int `json:"stale"`
	Rejected   int `json:"rejected"`
}

type Run struct {
	ID         string     `json:"id"`
	SupplierID string     `json:"supplier_id"`
	SourceURL  string     `json:"source_url"`
	UpdateID   string     `json:"update_id"`
	Status     string     `json:"status"`
	NextCursor *string    `json:"next_cursor"`
	Counts     Counts     `json:"counts"`
	OpenIssues int        `json:"open_issues"`
	Attempts   int        `json:"attempts"`
	LastError  string     `json:"last_error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type Change struct {
	ID     string   `json:"id"`
	RunID  string   `json:"run_id"`
	Cursor string   `json:"cursor"`
	Index  int      `json:"index"`
	Kind   string   `json:"kind"`
	Before *Product `json:"before,omitempty"`
	After  Product  `json:"after"`
}

type Issue struct {
	ID         string          `json:"id"`
	RunID      string          `json:"run_id"`
	SupplierID string          `json:"supplier_id"`
	SKU        string          `json:"sku,omitempty"`
	Version    int64           `json:"version,omitempty"`
	Cursor     string          `json:"cursor"`
	Index      int             `json:"index"`
	Code       string          `json:"code"`
	Message    string          `json:"message"`
	Raw        json.RawMessage `json:"raw"`
	Resolution string          `json:"resolution,omitempty"`
	ResolvedBy string          `json:"resolved_by,omitempty"`
	ResolvedAt *time.Time      `json:"resolved_at,omitempty"`
}

func validID(s string) bool { return identifier.MatchString(s) }

// DecodeStrict also rejects concatenated JSON documents.
func DecodeStrict(data []byte, into any) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON document")
	}
	return nil
}

func parseRecord(raw json.RawMessage) (Record, string, error) {
	var r Record
	if err := DecodeStrict(raw, &r); err != nil {
		return r, "invalid_record", err
	}
	if !validID(r.SKU) {
		return r, "invalid_identity", errors.New("sku must be 1-128 letters, digits, dots, underscores or hyphens")
	}
	if r.Version < 1 {
		return r, "invalid_version", errors.New("version must be a positive integer")
	}
	if r.Withdrawn {
		r.Name, r.PriceCents, r.Currency = "", nil, ""
		return r, "", nil
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" || len(r.Name) > 300 {
		return r, "invalid_details", errors.New("active product needs a name of 1-300 bytes")
	}
	if r.PriceCents == nil || *r.PriceCents < 0 {
		return r, "invalid_details", errors.New("active product needs a non-negative integer price_cents")
	}
	if !currencyCode.MatchString(r.Currency) {
		return r, "invalid_details", fmt.Errorf("currency %q must be three uppercase letters", r.Currency)
	}
	return r, "", nil
}

func sameDetails(a, b Record) bool {
	if a.SKU != b.SKU || a.Name != b.Name || a.Currency != b.Currency || a.Withdrawn != b.Withdrawn {
		return false
	}
	if a.PriceCents == nil || b.PriceCents == nil {
		return a.PriceCents == b.PriceCents
	}
	return *a.PriceCents == *b.PriceCents
}
