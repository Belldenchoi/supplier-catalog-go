package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var buckets = []string{"meta", "products", "runs", "pages", "changes", "issues"}

type Store struct{ db *bolt.DB }

// Open uses bbolt's file lock and normal fsync behavior. Never enable NoSync.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open catalog (only one CLI process may use it at once): %w", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		meta := tx.Bucket([]byte("meta"))
		version := meta.Get([]byte("schema"))
		if version != nil && string(version) != "1" {
			return errors.New("unsupported database schema")
		}
		return meta.Put([]byte("schema"), []byte("1"))
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error    { return s.db.Close() }
func key(parts ...string) []byte { return []byte(strings.Join(parts, "\x00")) }

func put(tx *bolt.Tx, bucket string, k []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return tx.Bucket([]byte(bucket)).Put(k, data)
}

func get(tx *bolt.Tx, bucket string, k []byte, value any) error {
	data := tx.Bucket([]byte(bucket)).Get(k)
	if data == nil {
		return ErrNotFound
	}
	return json.Unmarshal(data, value)
}

func (s *Store) Start(supplierID, sourceURL, updateID string) (Run, error) {
	if !validID(supplierID) || !validID(updateID) {
		return Run{}, errors.New("supplier and update IDs must be 1-128 letters, digits, dots, underscores or hyphens")
	}
	u, err := url.Parse(sourceURL)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Run{}, errors.New("source must be an HTTP(S) base URL without credentials, query or fragment")
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Run{}, err
	}
	first, now := "0", time.Now().UTC()
	r := Run{ID: hex.EncodeToString(id[:]), SupplierID: supplierID, SourceURL: strings.TrimRight(sourceURL, "/"), UpdateID: updateID, Status: "running", NextCursor: &first, StartedAt: now, UpdatedAt: now}
	err = s.db.Update(func(tx *bolt.Tx) error { return put(tx, "runs", []byte(r.ID), r) })
	return r, err
}

func (s *Store) Run(id string) (Run, error) {
	var r Run
	err := s.db.View(func(tx *bolt.Tx) error { return get(tx, "runs", []byte(id), &r) })
	return r, err
}

func (s *Store) Runs() ([]Run, error) {
	result := []Run{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("runs")).ForEach(func(_, data []byte) error {
			var r Run
			if err := json.Unmarshal(data, &r); err != nil {
				return err
			}
			result = append(result, r)
			return nil
		})
	})
	return result, err
}

func (s *Store) Products(supplierID string, includeWithdrawn bool) ([]Product, error) {
	result := []Product{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("products")).ForEach(func(_, data []byte) error {
			var p Product
			if err := json.Unmarshal(data, &p); err != nil {
				return err
			}
			if (supplierID == "" || p.SupplierID == supplierID) && (includeWithdrawn || !p.Withdrawn) {
				result = append(result, p)
			}
			return nil
		})
	})
	return result, err
}

func (s *Store) Changes(runID string) ([]Change, error) {
	result := []Change{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("changes")).ForEach(func(_, data []byte) error {
			var c Change
			if err := json.Unmarshal(data, &c); err != nil {
				return err
			}
			if runID == "" || c.RunID == runID {
				result = append(result, c)
			}
			return nil
		})
	})
	return result, err
}

func (s *Store) Issues(runID string, includeResolved bool) ([]Issue, error) {
	result := []Issue{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("issues")).ForEach(func(_, data []byte) error {
			var issue Issue
			if err := json.Unmarshal(data, &issue); err != nil {
				return err
			}
			if (runID == "" || issue.RunID == runID) && (includeResolved || issue.ResolvedAt == nil) {
				result = append(result, issue)
			}
			return nil
		})
	})
	return result, err
}

func finishStatus(r *Run) {
	if r.NextCursor != nil {
		return
	}
	r.Status = "completed"
	if r.OpenIssues > 0 {
		r.Status = "finished_with_issues"
	}
	if r.FinishedAt == nil {
		now := time.Now().UTC()
		r.FinishedAt = &now
	}
}

// resolveIssue is called within the same transaction that fixes a product.
func resolveIssue(tx *bolt.Tx, issue Issue, by, resolution string, current *Run) error {
	now := time.Now().UTC()
	issue.ResolvedAt, issue.ResolvedBy, issue.Resolution = &now, by, resolution
	if err := put(tx, "issues", []byte(issue.ID), issue); err != nil {
		return err
	}
	if current != nil && issue.RunID == current.ID {
		current.OpenIssues--
		return nil
	}
	var owner Run
	if err := get(tx, "runs", []byte(issue.RunID), &owner); err != nil {
		return err
	}
	owner.OpenIssues--
	owner.UpdatedAt = now
	finishStatus(&owner)
	return put(tx, "runs", []byte(owner.ID), owner)
}

func (s *Store) Acknowledge(issueID, note string) error {
	if strings.TrimSpace(note) == "" {
		return errors.New("a resolution note is required")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		var issue Issue
		if err := get(tx, "issues", []byte(issueID), &issue); err != nil {
			return err
		}
		if issue.ResolvedAt != nil {
			return nil
		}
		return resolveIssue(tx, issue, "staff", "acknowledged: "+strings.TrimSpace(note), nil)
	})
}

func (s *Store) beginAttempt(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var r Run
		if err := get(tx, "runs", []byte(id), &r); err != nil {
			return err
		}
		if r.NextCursor == nil {
			return nil
		}
		r.Status, r.LastError, r.UpdatedAt = "running", "", time.Now().UTC()
		r.Attempts++
		return put(tx, "runs", []byte(id), r)
	})
}

func (s *Store) markStopped(id, cursor, status string, cause error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var r Run
		if err := get(tx, "runs", []byte(id), &r); err != nil {
			return err
		}
		if r.NextCursor == nil || *r.NextCursor != cursor {
			return nil
		}
		r.Status, r.LastError, r.UpdatedAt = status, cause.Error(), time.Now().UTC()
		return put(tx, "runs", []byte(id), r)
	})
}

// commitPage is the recovery boundary: products, audit, issues and cursor either
// all commit or all roll back. The expected cursor prevents concurrent replay.
func (s *Store) commitPage(runID, expected string, page Page) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var r Run
		if err := get(tx, "runs", []byte(runID), &r); err != nil {
			return err
		}
		if r.NextCursor == nil || *r.NextCursor != expected {
			return errProgressChanged
		}
		if page.SupplierID != r.SupplierID || page.UpdateID != r.UpdateID || page.Cursor != expected || page.Records == nil {
			return errors.New("supplier response identity/cursor mismatch or missing records array")
		}
		if page.NextCursor != nil {
			if *page.NextCursor == "" || len(*page.NextCursor) > 512 || strings.ContainsRune(*page.NextCursor, 0) {
				return errors.New("invalid next cursor")
			}
			if *page.NextCursor == expected || tx.Bucket([]byte("pages")).Get(key(runID, *page.NextCursor)) != nil {
				return errors.New("supplier pagination cycle detected")
			}
		}
		for i, raw := range page.Records {
			r.Counts.Records++
			record, code, validationErr := parseRecord(raw)
			var before *Product
			// An identifiable older version is stale even if its old details are
			// invalid. Replaying a superseded bad record must not reopen an issue.
			if validationErr == nil || code == "invalid_details" {
				var old Product
				err := get(tx, "products", key(r.SupplierID, record.SKU), &old)
				if err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
				if err == nil {
					before = &old
					if record.Version < old.Version {
						r.Counts.Stale++
						continue
					}
					if record.Version == old.Version && validationErr == nil {
						if sameDetails(record, old.Record) {
							r.Counts.Duplicates++
							continue
						}
						code, validationErr = "version_conflict", errors.New("same SKU/version has conflicting content; request a strictly higher version")
					}
				}
			}
			if validationErr != nil {
				issue := Issue{ID: fmt.Sprintf("%s-%06d-%06d", r.ID, r.Counts.Pages, i), RunID: r.ID, SupplierID: r.SupplierID, SKU: record.SKU, Version: record.Version, Cursor: expected, Index: i, Code: code, Message: validationErr.Error(), Raw: raw}
				if err := put(tx, "issues", []byte(issue.ID), issue); err != nil {
					return err
				}
				r.OpenIssues++
				r.Counts.Rejected++
				continue
			}
			kind := "added"
			switch {
			case record.Withdrawn && (before == nil || !before.Withdrawn):
				kind = "withdrawn"
				r.Counts.Withdrawn++
			case before == nil:
				r.Counts.Added++
			case before.Withdrawn && !record.Withdrawn:
				kind = "restored"
				r.Counts.Restored++
			case sameDetails(record, before.Record):
				kind = "unchanged"
				r.Counts.Unchanged++
			default:
				kind = "updated"
				r.Counts.Updated++
			}
			product := Product{Record: record, SupplierID: r.SupplierID, LastRunID: r.ID}
			if err := put(tx, "products", key(r.SupplierID, record.SKU), product); err != nil {
				return err
			}
			change := Change{ID: fmt.Sprintf("%s-%06d-%06d", r.ID, r.Counts.Pages, i), RunID: r.ID, Cursor: expected, Index: i, Kind: kind, Before: before, After: product}
			if err := put(tx, "changes", []byte(change.ID), change); err != nil {
				return err
			}
			// Collect first: avoid mutating a bbolt bucket during ForEach.
			var fixed []Issue
			if err := tx.Bucket([]byte("issues")).ForEach(func(_, data []byte) error {
				var issue Issue
				if err := json.Unmarshal(data, &issue); err != nil {
					return err
				}
				if issue.ResolvedAt == nil && issue.SupplierID == r.SupplierID && issue.SKU == record.SKU && validID(issue.SKU) && issue.Version > 0 && record.Version > issue.Version {
					fixed = append(fixed, issue)
				}
				return nil
			}); err != nil {
				return err
			}
			for _, issue := range fixed {
				if err := resolveIssue(tx, issue, r.ID, "superseded by a valid higher product version", &r); err != nil {
					return err
				}
			}
		}
		if err := tx.Bucket([]byte("pages")).Put(key(runID, expected), []byte{1}); err != nil {
			return err
		}
		r.Counts.Pages++
		r.NextCursor, r.LastError, r.Status, r.UpdatedAt = page.NextCursor, "", "running", time.Now().UTC()
		finishStatus(&r)
		return put(tx, "runs", []byte(runID), r)
	})
}
