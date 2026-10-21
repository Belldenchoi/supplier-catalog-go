package supplier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"suppliercatalog/internal/catalog"
)

const MaxPageBytes = 2 << 20

type HTTPSource struct{ Client *http.Client }

func (s HTTPSource) Fetch(ctx context.Context, run catalog.Run, cursor string) (catalog.Page, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	endpoint := run.SourceURL + "/v1/updates/" + url.PathEscape(run.UpdateID) + "/batches?" + url.Values{"cursor": {cursor}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return catalog.Page{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return catalog.Page{}, fmt.Errorf("fetch supplier: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return catalog.Page{}, fmt.Errorf("supplier returned HTTP %d; resume after the supplier recovers", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxPageBytes+1))
	if err != nil {
		return catalog.Page{}, fmt.Errorf("read supplier response: %w", err)
	}
	if len(data) > MaxPageBytes {
		return catalog.Page{}, fmt.Errorf("batch exceeds %d bytes", MaxPageBytes)
	}
	var page catalog.Page
	if err := catalog.DecodeStrict(data, &page); err != nil {
		return page, fmt.Errorf("invalid batch JSON: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return page, err
	}
	// A missing terminal marker must never be mistaken for successful completion.
	for _, name := range []string{"supplier_id", "update_id", "cursor", "next_cursor", "records"} {
		if _, ok := fields[name]; !ok {
			return page, fmt.Errorf("batch missing required field %q", name)
		}
	}
	if len(page.Records) > 2000 {
		return page, fmt.Errorf("batch has more than 2000 records")
	}
	return page, nil
}
