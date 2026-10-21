package catalog

import (
	"context"
	"errors"
	"fmt"
)

type Source interface {
	Fetch(context.Context, Run, string) (Page, error)
}

type Engine struct {
	Store  *Store
	Source Source
}

// Resume never trusts a previous 'running' marker; durable cursor is authoritative.
// maxPages is a reproducible graceful-interruption hook, zero means no limit.
func (e Engine) Resume(ctx context.Context, id string, maxPages int) error {
	if maxPages < 0 {
		return errors.New("max-pages must be non-negative")
	}
	if err := e.Store.beginAttempt(id); err != nil {
		return err
	}
	processed := 0
	for {
		r, err := e.Store.Run(id)
		if err != nil {
			return err
		}
		if r.NextCursor == nil {
			if r.OpenIssues > 0 {
				return ErrAttention
			}
			return nil
		}
		cursor := *r.NextCursor
		var stop error
		if ctx.Err() != nil {
			stop = ctx.Err()
		} else if maxPages > 0 && processed >= maxPages {
			stop = ErrInterrupted
		}
		if stop != nil {
			return errors.Join(stop, e.Store.markStopped(id, cursor, "interrupted", stop))
		}
		page, err := e.Source.Fetch(ctx, r, cursor)
		if err == nil {
			err = e.Store.commitPage(id, cursor, page)
		}
		if errors.Is(err, errProgressChanged) {
			continue
		}
		if err != nil {
			status := "blocked"
			if ctx.Err() != nil {
				status = "interrupted"
			}
			cause := fmt.Errorf("cursor %q: %w", cursor, err)
			return errors.Join(cause, e.Store.markStopped(id, cursor, status, cause))
		}
		processed++
	}
}
