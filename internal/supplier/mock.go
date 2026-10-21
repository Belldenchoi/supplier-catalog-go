package supplier

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"suppliercatalog/internal/catalog"
)

//go:embed fixtures/*.json
var fixtures embed.FS

type Snapshot struct {
	SupplierID string              `json:"supplier_id"`
	UpdateID   string              `json:"update_id"`
	Batches    [][]json.RawMessage `json:"batches"`
}

type Mock struct {
	Files      fs.FS
	FailUpdate string
	FailCursor string
	FailCount  int
	mu         sync.Mutex
	failed     int
}

func (m *Mock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/healthz" {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "updates" || parts[3] != "batches" {
		http.NotFound(w, r)
		return
	}
	updateID := parts[2]
	// Restrict file names before accessing the optional custom fixture directory.
	for _, ch := range updateID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			http.NotFound(w, r)
			return
		}
	}
	if updateID == "" {
		http.NotFound(w, r)
		return
	}
	files := m.Files
	if files == nil {
		files, _ = fs.Sub(fixtures, "fixtures")
	}
	data, err := fs.ReadFile(files, updateID+".json")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var snapshot Snapshot
	if err := catalog.DecodeStrict(data, &snapshot); err != nil || snapshot.UpdateID != updateID {
		http.Error(w, "invalid fixture snapshot", http.StatusInternalServerError)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	index, err := strconv.Atoi(cursor)
	if err != nil || index < 0 || index >= len(snapshot.Batches) || strconv.Itoa(index) != cursor {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	fail := updateID == m.FailUpdate && cursor == m.FailCursor && m.failed < m.FailCount
	if fail {
		m.failed++
	}
	m.mu.Unlock()
	if fail {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "fictional supplier temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	var next *string
	if index+1 < len(snapshot.Batches) {
		value := fmt.Sprint(index + 1)
		next = &value
	}
	page := catalog.Page{SupplierID: snapshot.SupplierID, UpdateID: snapshot.UpdateID, Cursor: cursor, NextCursor: next, Records: snapshot.Batches[index]}
	_ = json.NewEncoder(w).Encode(page)
}
