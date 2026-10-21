package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"suppliercatalog/internal/catalog"
)

func TestHTTPValidation(t *testing.T) {
	valid := `{"supplier_id":"northstar","update_id":"baseline","cursor":"0","next_cursor":null,"records":[]}`
	for _, tc := range []struct {
		name, body string
		code       int
		ok         bool
	}{
		{"terminal empty", valid, 200, true},
		{"missing terminal marker", `{"supplier_id":"northstar","update_id":"baseline","cursor":"0","records":[]}`, 200, false},
		{"truncated", valid[:len(valid)-1], 200, false},
		{"unknown field", strings.Replace(valid, `"records":[]`, `"records":[],"typo":1`, 1), 200, false},
		{"multiple documents", valid + valid, 200, false},
		{"oversize", strings.Repeat(" ", MaxPageBytes+1), 200, false},
		{"unavailable", "offline", 503, false},
		{"not found", "missing", 404, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/updates/baseline/batches" || r.URL.Query().Get("cursor") != "0" {
					t.Error(r.URL)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := (HTTPSource{}).Fetch(context.Background(), catalog.Run{SourceURL: server.URL, UpdateID: "baseline"}, "0")
			if (err == nil) != tc.ok {
				t.Fatal(err)
			}
		})
	}
}

func TestHTTPTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	source := HTTPSource{Client: &http.Client{Timeout: 25 * time.Millisecond}}
	if _, err := source.Fetch(context.Background(), catalog.Run{SourceURL: server.URL, UpdateID: "baseline"}, "0"); err == nil {
		t.Fatal("timeout ignored")
	}
}

func TestFictionalSupplierEndToEnd(t *testing.T) {
	server := httptest.NewServer(&Mock{FailUpdate: "delta", FailCursor: "1", FailCount: 1})
	defer server.Close()
	s, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := catalog.Engine{Store: s, Source: HTTPSource{}}
	start := func(update string) catalog.Run {
		r, err := s.Start("northstar", server.URL, update)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	base := start("baseline")
	if err := e.Resume(context.Background(), base.ID, 0); err != nil {
		t.Fatal(err)
	}
	delta := start("delta")
	if err := e.Resume(context.Background(), delta.ID, 0); err == nil {
		t.Fatal("outage not observed")
	}
	delta, _ = s.Run(delta.ID)
	if delta.Status != "blocked" || delta.Counts.Pages != 1 || delta.Counts.Updated != 1 || delta.Counts.Withdrawn != 1 {
		t.Fatalf("outage: %+v", delta)
	}
	p, _ := s.Products("", true)
	if len(p) != 3 {
		t.Fatal("outage erased catalog")
	}
	if err := e.Resume(context.Background(), delta.ID, 0); !errors.Is(err, catalog.ErrAttention) {
		t.Fatal(err)
	}
	delta, _ = s.Run(delta.ID)
	if delta.Status != "finished_with_issues" || delta.Counts.Pages != 3 || delta.OpenIssues != 2 || delta.Counts.Duplicates != 2 || delta.Counts.Stale != 2 || delta.Counts.Added != 1 || delta.Counts.Updated != 2 {
		t.Fatalf("delta: %+v", delta)
	}
	fix := start("corrections")
	if err := e.Resume(context.Background(), fix.ID, 0); err != nil {
		t.Fatal(err)
	}
	delta, _ = s.Run(delta.ID)
	if delta.OpenIssues != 0 || delta.Status != "completed" {
		t.Fatalf("issues unresolved: %+v", delta)
	}
	replay := start("delta")
	if err := e.Resume(context.Background(), replay.ID, 0); err != nil {
		t.Fatalf("replay reopened issues: %v", err)
	}
	p, _ = s.Products("", true)
	if len(p) != 5 {
		t.Fatalf("duplicate products: %+v", p)
	}
	for _, product := range p {
		if product.SKU == "LAMP" && (!product.Withdrawn || product.Version != 4) {
			t.Fatal("withdrawal resurrected")
		}
	}
	// All fixture responses are ordinary JSON, usable without the importer.
	resp, err := http.Get(server.URL + "/v1/updates/baseline/batches?cursor=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page catalog.Page
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
}
