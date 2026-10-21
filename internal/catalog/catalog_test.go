package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

type sourceFunc func(context.Context, Run, string) (Page, error)

func (f sourceFunc) Fetch(ctx context.Context, r Run, c string) (Page, error) { return f(ctx, r, c) }
func str(s string) *string                                                    { return &s }
func raw(s string) json.RawMessage                                            { return json.RawMessage(s) }
func active(sku string, version int64, name string) json.RawMessage {
	return raw(fmt.Sprintf(`{"sku":%q,"version":%d,"name":%q,"price_cents":100,"currency":"USD","withdrawn":false}`, sku, version, name))
}
func tombstone(sku string, version int64) json.RawMessage {
	return raw(fmt.Sprintf(`{"sku":%q,"version":%d,"withdrawn":true}`, sku, version))
}
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func startTest(t *testing.T, s *Store, supplier string) Run {
	t.Helper()
	r, err := s.Start(supplier, "http://example.invalid", "test")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func page(r Run, cursor string, next *string, records ...json.RawMessage) Page {
	if records == nil {
		records = []json.RawMessage{}
	}
	return Page{SupplierID: r.SupplierID, UpdateID: r.UpdateID, Cursor: cursor, NextCursor: next, Records: records}
}
func importRecords(t *testing.T, s *Store, records ...json.RawMessage) Run {
	t.Helper()
	r := startTest(t, s, "northstar")
	if err := s.commitPage(r.ID, "0", page(r, "0", nil, records...)); err != nil {
		t.Fatal(err)
	}
	r, err := s.Run(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestVersionRules(t *testing.T) {
	s := openTest(t)
	r := importRecords(t, s, active("A", 1, "first"), active("A", 3, "new"), active("A", 3, "new"), active("A", 2, "stale"), tombstone("A", 5), active("A", 4, "must not restore"), tombstone("B", 10), active("B", 2, "unknown withdrawal stays"))
	products, err := s.Products("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 2 || products[0].Version != 5 || !products[0].Withdrawn || products[1].Version != 10 || !products[1].Withdrawn {
		t.Fatalf("products: %+v", products)
	}
	if r.Counts.Added != 1 || r.Counts.Updated != 1 || r.Counts.Duplicates != 1 || r.Counts.Stale != 3 || r.Counts.Withdrawn != 2 || r.Status != "completed" {
		t.Fatalf("run: %+v", r)
	}
	visible, _ := s.Products("", false)
	if len(visible) != 0 {
		t.Fatal("withdrawals visible in active catalog")
	}
	r = importRecords(t, s, active("A", 6, "explicit reinstatement"))
	if r.Counts.Restored != 1 {
		t.Fatalf("restore: %+v", r)
	}
}

func TestOrderIndependentAndRepeatedDelivery(t *testing.T) {
	for seed := int64(0); seed < 30; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			s := openTest(t)
			records := []json.RawMessage{active("A", 1, "one"), active("A", 3, "three"), tombstone("A", 5), active("A", 4, "four"), active("B", 2, "two"), active("B", 1, "one"), tombstone("A", 5)}
			rand.New(rand.NewSource(seed)).Shuffle(len(records), func(i, j int) { records[i], records[j] = records[j], records[i] })
			importRecords(t, s, records...)
			before, _ := s.Products("", true)
			r := importRecords(t, s, records...)
			after, _ := s.Products("", true)
			if !reflect.DeepEqual(before, after) || len(after) != 2 || after[0].Version != 5 || !after[0].Withdrawn || after[1].Version != 2 {
				t.Fatalf("non-idempotent: before=%+v after=%+v", before, after)
			}
			if r.Counts.Added+r.Counts.Updated+r.Counts.Withdrawn+r.Counts.Restored != 0 {
				t.Fatalf("replay changed data: %+v", r.Counts)
			}
		})
	}
}

func TestIssuesCorrectionsAndAcknowledgment(t *testing.T) {
	s := openTest(t)
	r := importRecords(t, s, active("A", 3, "canonical"), active("A", 3, "conflict"), raw(`{"sku":"B","version":1,"name":"bad price","price_cents":-1,"currency":"USD"}`), raw(`{"sku":"../bad","version":1}`))
	if r.Status != "finished_with_issues" || r.OpenIssues != 3 || r.Counts.Rejected != 3 || r.FinishedAt == nil {
		t.Fatalf("run: %+v", r)
	}
	issues, _ := s.Issues(r.ID, false)
	if len(issues) != 3 || issues[0].Code != "version_conflict" {
		t.Fatalf("issues: %+v", issues)
	}
	products, _ := s.Products("", true)
	if products[0].Name != "canonical" {
		t.Fatal("same-version conflict overwrote product")
	}
	importRecords(t, s, active("A", 4, "fixed"), active("B", 2, "fixed"))
	r, _ = s.Run(r.ID)
	if r.OpenIssues != 1 || r.Status != "finished_with_issues" {
		t.Fatalf("correction: %+v", r)
	}
	remaining, _ := s.Issues(r.ID, false)
	if err := s.Acknowledge(remaining[0].ID, ""); err == nil {
		t.Fatal("blank acknowledgment allowed")
	}
	if err := s.Acknowledge(remaining[0].ID, "Supplier confirmed this identifier is unusable; discarded."); err != nil {
		t.Fatal(err)
	}
	if err := s.Acknowledge(remaining[0].ID, "repeat is idempotent"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Run(r.ID)
	if r.OpenIssues != 0 || r.Status != "completed" {
		t.Fatalf("ack: %+v", r)
	}
	all, _ := s.Issues(r.ID, true)
	if len(all) != 3 || all[0].ResolvedAt == nil || all[2].ResolvedBy != "staff" {
		t.Fatalf("audit lost: %+v", all)
	}
	// The invalid older payload is already superseded: replay cannot reopen it.
	replay := importRecords(t, s, raw(`{"sku":"B","version":1,"name":"bad price","price_cents":-1,"currency":"USD"}`))
	if replay.Counts.Stale != 1 || replay.OpenIssues != 0 {
		t.Fatalf("old invalid record reopened: %+v", replay)
	}
}

func TestNewerVersionResolvesIssueWithinSamePage(t *testing.T) {
	s := openTest(t)
	r := importRecords(t, s, active("A", 1, "one"), active("A", 1, "conflict"), active("A", 2, "two"))
	if r.OpenIssues != 0 || r.Counts.Rejected != 1 || r.Status != "completed" {
		t.Fatalf("same-page resolution: %+v", r)
	}
}

func TestSupplierIsolation(t *testing.T) {
	s := openTest(t)
	for _, supplier := range []string{"northstar", "other"} {
		r := startTest(t, s, supplier)
		if err := s.commitPage(r.ID, "0", page(r, "0", nil, active("A", 1, supplier))); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := s.Products("", true)
	if len(p) != 2 {
		t.Fatalf("supplier identities collided: %+v", p)
	}
	p, _ = s.Products("other", false)
	if len(p) != 1 || p[0].Name != "other" {
		t.Fatal(p)
	}
}

func TestOutageAndRestartResumeAtCommittedCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r := startTest(t, s, "northstar")
	e := Engine{Store: s, Source: sourceFunc(func(_ context.Context, r Run, c string) (Page, error) {
		if c == "0" {
			return page(r, c, str("1"), active("A", 1, "saved")), nil
		}
		return Page{}, errors.New("supplier offline")
	})}
	if err := e.Resume(context.Background(), r.ID, 0); err == nil {
		t.Fatal("outage was success")
	}
	r, _ = s.Run(r.ID)
	if r.Status != "blocked" || r.Counts.Pages != 1 || r.NextCursor == nil || *r.NextCursor != "1" || r.FinishedAt != nil {
		t.Fatalf("partial: %+v", r)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e = Engine{Store: s, Source: sourceFunc(func(_ context.Context, r Run, c string) (Page, error) {
		if c != "1" {
			t.Fatalf("refetched committed page %q", c)
		}
		return page(r, c, nil, active("B", 1, "new")), nil
	})}
	if err := e.Resume(context.Background(), r.ID, 0); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Run(r.ID)
	p, _ := s.Products("", true)
	if r.Status != "completed" || r.Counts.Pages != 2 || r.Counts.Added != 2 || r.Attempts != 2 || len(p) != 2 || r.LastError != "" {
		t.Fatalf("resume: %+v products=%+v", r, p)
	}
	if err := e.Resume(context.Background(), r.ID, 0); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Run(r.ID)
	if !reflect.DeepEqual(r, after) {
		t.Fatal("resuming terminal run changed it")
	}
}

func TestGracefulInterruptionAndCancellation(t *testing.T) {
	for _, mode := range []string{"max-pages", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			s := openTest(t)
			r := startTest(t, s, "northstar")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			limit := 1
			if mode == "canceled" {
				cancel()
				limit = 0
			}
			e := Engine{Store: s, Source: sourceFunc(func(_ context.Context, r Run, c string) (Page, error) {
				return page(r, c, str("1"), active("A", 1, "saved")), nil
			})}
			err := e.Resume(ctx, r.ID, limit)
			if mode == "max-pages" && !errors.Is(err, ErrInterrupted) {
				t.Fatal(err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			r, _ = s.Run(r.ID)
			if r.Status != "interrupted" || r.FinishedAt != nil {
				t.Fatal(r)
			}
		})
	}
}

func TestEnvelopeValidationRollsBackPage(t *testing.T) {
	for _, mode := range []string{"wrong-supplier", "wrong-update", "wrong-cursor", "missing-records", "self-cycle", "empty-cursor", "back-cycle"} {
		t.Run(mode, func(t *testing.T) {
			s := openTest(t)
			r := startTest(t, s, "northstar")
			expected := "0"
			if mode == "back-cycle" {
				if err := s.commitPage(r.ID, "0", page(r, "0", str("1"), active("KEEP", 1, "saved"))); err != nil {
					t.Fatal(err)
				}
				expected = "1"
			}
			p := page(r, expected, nil, active("A", 1, "must rollback"))
			switch mode {
			case "wrong-supplier":
				p.SupplierID = "imposter"
			case "wrong-update":
				p.UpdateID = "changed"
			case "wrong-cursor":
				p.Cursor = "oops"
			case "missing-records":
				p.Records = nil
			case "self-cycle":
				p.NextCursor = &expected
			case "empty-cursor":
				p.NextCursor = str("")
			case "back-cycle":
				p.NextCursor = str("0")
			}
			if err := s.commitPage(r.ID, expected, p); err == nil {
				t.Fatal("bad envelope accepted")
			}
			r, _ = s.Run(r.ID)
			products, _ := s.Products("", true)
			want := 0
			if mode == "back-cycle" {
				want = 1
			}
			if len(products) != want || *r.NextCursor != expected || r.Counts.Pages != want {
				t.Fatalf("partial mutation: %+v %+v", r, products)
			}
		})
	}
}

func TestPageTransactionRollsBackOnStorageError(t *testing.T) {
	s := openTest(t)
	r := startTest(t, s, "northstar")
	// Force an error after the first product write, when scanning issue storage.
	if err := s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("issues")).Put([]byte("corrupt"), []byte("not-json")) }); err != nil {
		t.Fatal(err)
	}
	if err := s.commitPage(r.ID, "0", page(r, "0", nil, active("A", 1, "must rollback"))); err == nil {
		t.Fatal("storage error swallowed")
	}
	p, _ := s.Products("", true)
	changes, _ := s.Changes(r.ID)
	r, _ = s.Run(r.ID)
	if len(p) != 0 || len(changes) != 0 || r.Counts.Pages != 0 || *r.NextCursor != "0" {
		t.Fatalf("transaction leaked changes: %+v %+v", p, r)
	}
}

func TestCommitSameCursorOnlyOnce(t *testing.T) {
	s := openTest(t)
	r := startTest(t, s, "northstar")
	p := page(r, "0", str("1"), active("A", 1, "one"))
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- s.commitPage(r.ID, "0", p) }()
	}
	one, two := <-results, <-results
	if !((one == nil && errors.Is(two, errProgressChanged)) || (two == nil && errors.Is(one, errProgressChanged))) {
		t.Fatalf("%v / %v", one, two)
	}
	r, _ = s.Run(r.ID)
	if r.Counts.Pages != 1 || r.Counts.Added != 1 {
		t.Fatal(r)
	}
}

func TestEmptyTerminalBatch(t *testing.T) {
	s := openTest(t)
	r := importRecords(t, s)
	if r.Status != "completed" || r.Counts.Pages != 1 || r.Counts.Records != 0 {
		t.Fatal(r)
	}
}

func TestRecordValidation(t *testing.T) {
	cases := []struct {
		data  string
		valid bool
	}{
		{`{"sku":"A","version":1,"withdrawn":true}`, true},
		{`{"sku":"A","version":1,"name":" Free ","price_cents":0,"currency":"USD"}`, true},
		{`null`, false}, {`{}`, false}, {`{"sku":"../A","version":1}`, false},
		{`{"sku":"A","version":0,"withdrawn":true}`, false},
		{`{"sku":"A","version":1,"name":"A","currency":"USD"}`, false},
		{`{"sku":"A","version":1,"name":"A","price_cents":1.25,"currency":"USD"}`, false},
		{`{"sku":"A","version":1,"name":"A","price_cents":-1,"currency":"USD"}`, false},
		{`{"sku":"A","version":1,"name":" ","price_cents":1,"currency":"USD"}`, false},
		{`{"sku":"A","version":1,"name":"A","price_cents":1,"currency":"usd"}`, false},
		{`{"sku":"A","version":1,"withdrawn":true,"typo":true}`, false},
		{`{"sku":"A","version":9223372036854775808,"withdrawn":true}`, false},
		{`{"sku":"A","version":1,"withdrawn":true} {}`, false},
	}
	for _, tc := range cases {
		_, _, err := parseRecord(raw(tc.data))
		if (err == nil) != tc.valid {
			t.Errorf("%s => %v", tc.data, err)
		}
	}
}

// A real subprocess is killed while fetching the next page or while holding an
// uncommitted DB transaction. These tests cover restart after abrupt termination.
func TestProcessCrashRecovery(t *testing.T) {
	for _, mode := range []string{"fetch", "transaction"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "catalog.db")
			marker := filepath.Join(dir, "ready")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			r := startTest(t, s, "northstar")
			if err := s.commitPage(r.ID, "0", page(r, "0", str("1"), active("A", 1, "committed"))); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
			cmd.Env = append(os.Environ(), "CATALOG_CRASH_HELPER="+mode, "CATALOG_CRASH_DB="+path, "CATALOG_CRASH_RUN="+r.ID, "CATALOG_CRASH_MARKER="+marker)
			var log strings.Builder
			cmd.Stdout = &log
			cmd.Stderr = &log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill() }()
			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("helper not ready: %s", log.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			p, _ := s.Products("", true)
			r, _ = s.Run(r.ID)
			if len(p) != 1 || p[0].Name != "committed" || r.Counts.Pages != 1 || r.NextCursor == nil || *r.NextCursor != "1" || r.FinishedAt != nil {
				t.Fatalf("crash lost committed work or leaked uncommitted work: %+v %+v", p, r)
			}
			e := Engine{Store: s, Source: sourceFunc(func(_ context.Context, r Run, c string) (Page, error) {
				if c != "1" {
					t.Fatalf("wrong recovery cursor %q", c)
				}
				return page(r, c, nil, active("B", 1, "resumed")), nil
			})}
			if err := e.Resume(context.Background(), r.ID, 0); err != nil {
				t.Fatal(err)
			}
			r, _ = s.Run(r.ID)
			if r.Status != "completed" || r.Counts.Added != 2 {
				t.Fatal(r)
			}
		})
	}
}

func TestCrashHelper(t *testing.T) {
	mode := os.Getenv("CATALOG_CRASH_HELPER")
	if mode == "" {
		return
	}
	s, err := Open(os.Getenv("CATALOG_CRASH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ready := func() {
		if err := os.WriteFile(os.Getenv("CATALOG_CRASH_MARKER"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Second)
		}
	}
	id := os.Getenv("CATALOG_CRASH_RUN")
	if mode == "transaction" {
		err = s.db.Update(func(tx *bolt.Tx) error {
			var p Product
			if err := get(tx, "products", key("northstar", "A"), &p); err != nil {
				return err
			}
			p.Name = "uncommitted"
			p.Version = 99
			if err := put(tx, "products", key("northstar", "A"), p); err != nil {
				return err
			}
			var r Run
			if err := get(tx, "runs", []byte(id), &r); err != nil {
				return err
			}
			r.NextCursor = nil
			r.Status = "completed"
			r.Counts.Pages = 99
			if err := put(tx, "runs", []byte(id), r); err != nil {
				return err
			}
			ready()
			return nil
		})
	} else {
		e := Engine{Store: s, Source: sourceFunc(func(context.Context, Run, string) (Page, error) { ready(); return Page{}, nil })}
		err = e.Resume(context.Background(), id, 0)
	}
	t.Fatalf("crash helper should be killed: %v", err)
}
