package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"suppliercatalog/internal/catalog"
	"suppliercatalog/internal/supplier"
)

func TestCLIWorkflowAcrossInvocations(t *testing.T) {
	server := httptest.NewServer(&supplier.Mock{})
	defer server.Close()
	db := filepath.Join(t.TempDir(), "catalog.db")
	call := func(args ...string) ([]byte, error) {
		var out, stderr bytes.Buffer
		args = append(args, "--db", db)
		err := execute(context.Background(), args, &out, &stderr)
		return out.Bytes(), err
	}
	data, err := call("start", "--source", server.URL, "--update", "baseline", "--max-pages", "1")
	if !errors.Is(err, catalog.ErrInterrupted) {
		t.Fatal(err)
	}
	var r catalog.Run
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "interrupted" || r.Counts.Pages != 1 {
		t.Fatal(r)
	}
	data, err = call("resume", "--run", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Status != "completed" || r.Counts.Pages != 2 {
		t.Fatal(r)
	}
	if _, err := call("changes", "--run", r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := call("issues", "--run", "does-not-exist"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := call("start", "--source", server.URL, "--update", "baseline", "--max-pages", "-1"); err == nil {
		t.Fatal("invalid limit accepted")
	}
	if _, err := call("resume"); err == nil {
		t.Fatal("missing run accepted")
	}
}
