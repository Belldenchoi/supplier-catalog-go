package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"suppliercatalog/internal/catalog"
	"suppliercatalog/internal/supplier"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		switch {
		case errors.Is(err, catalog.ErrAttention):
			os.Exit(2)
		case errors.Is(err, catalog.ErrInterrupted), errors.Is(err, context.Canceled):
			os.Exit(3)
		default:
			os.Exit(1)
		}
	}
}

const usage = `Supplier catalog — durable, resumable batch updates

  catalog supplier [--addr 127.0.0.1:8091] [--fixtures directory]
                   [--fail-update delta --fail-cursor 1 --fail-count 1]
  catalog start --update baseline [--supplier northstar] [--source URL] [--max-pages N]
  catalog resume --run ID [--max-pages N]
  catalog status [--run ID]
  catalog products [--supplier ID] [--all]
  catalog changes [--run ID]
  catalog issues [--run ID] [--all]
  catalog acknowledge --issue ID --note "reason"

All database commands accept --db PATH (default data/catalog.db).
Default supplier URL: http://127.0.0.1:8091
JSON output. Exit codes: 0 success, 1 blocked/error, 2 unresolved issues,
3 interrupted. Inspect status/last_error, fix the cause and resume the same run.
`

func execute(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, err := fmt.Fprint(out, usage)
		return err
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errOut)
	if command == "supplier" {
		return serveSupplier(ctx, f, args[1:], errOut)
	}
	valid := map[string]bool{"start": true, "resume": true, "status": true, "products": true, "changes": true, "issues": true, "acknowledge": true}
	if !valid[command] {
		return fmt.Errorf("unknown command %q; use catalog help", command)
	}
	db := f.String("db", "data/catalog.db", "database file")
	var runID, supplierID, sourceURL, updateID, issueID, note string
	var all bool
	var maxPages int
	switch command {
	case "start":
		f.StringVar(&supplierID, "supplier", "northstar", "supplier ID")
		f.StringVar(&sourceURL, "source", "http://127.0.0.1:8091", "supplier HTTP base URL")
		f.StringVar(&updateID, "update", "", "immutable supplier update ID (required)")
		f.IntVar(&maxPages, "max-pages", 0, "interrupt after N batches; zero is unlimited")
	case "resume":
		f.StringVar(&runID, "run", "", "run ID (required)")
		f.IntVar(&maxPages, "max-pages", 0, "interrupt after N additional batches")
	case "status", "changes":
		f.StringVar(&runID, "run", "", "filter by run ID")
	case "issues":
		f.StringVar(&runID, "run", "", "filter by run ID")
		f.BoolVar(&all, "all", false, "include resolved issues")
	case "products":
		f.StringVar(&supplierID, "supplier", "", "filter by supplier ID")
		f.BoolVar(&all, "all", false, "include withdrawal tombstones")
	case "acknowledge":
		f.StringVar(&issueID, "issue", "", "issue ID (required)")
		f.StringVar(&note, "note", "", "resolution explanation (required)")
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments; put options after the command")
	}
	if maxPages < 0 {
		return errors.New("max-pages must be non-negative")
	}
	if command == "start" && updateID == "" {
		return errors.New("--update is required")
	}
	if command == "resume" && runID == "" {
		return errors.New("--run is required")
	}
	if command == "acknowledge" && (issueID == "" || note == "") {
		return errors.New("--issue and --note are required")
	}
	store, err := catalog.Open(*db)
	if err != nil {
		return err
	}
	defer store.Close()
	encode := func(value any, err error) error {
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	switch command {
	case "start", "resume":
		if command == "start" {
			r, err := store.Start(supplierID, sourceURL, updateID)
			if err != nil {
				return err
			}
			runID = r.ID
			fmt.Fprintln(errOut, "started run", runID)
		}
		engine := catalog.Engine{Store: store, Source: supplier.HTTPSource{}}
		runErr := engine.Resume(ctx, runID, maxPages)
		r, err := store.Run(runID)
		return errors.Join(runErr, encode(r, err))
	case "status":
		if runID != "" {
			r, err := store.Run(runID)
			return encode(r, err)
		}
		runs, err := store.Runs()
		sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.Before(runs[j].StartedAt) })
		return encode(runs, err)
	case "products":
		p, err := store.Products(supplierID, all)
		return encode(p, err)
	case "changes", "issues":
		if runID != "" {
			if _, err := store.Run(runID); err != nil {
				return err
			}
		}
		if command == "changes" {
			c, err := store.Changes(runID)
			return encode(c, err)
		}
		i, err := store.Issues(runID, all)
		return encode(i, err)
	case "acknowledge":
		if err := store.Acknowledge(issueID, note); err != nil {
			return err
		}
		return encode(map[string]string{"issue_id": issueID, "status": "resolved"}, nil)
	}
	return nil
}

func serveSupplier(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	addr := f.String("addr", "127.0.0.1:8091", "listen address")
	fixtures := f.String("fixtures", "", "optional directory of snapshot JSON files")
	mock := &supplier.Mock{}
	f.StringVar(&mock.FailUpdate, "fail-update", "delta", "update to fail")
	f.StringVar(&mock.FailCursor, "fail-cursor", "", "batch cursor to fail (disabled by default)")
	f.IntVar(&mock.FailCount, "fail-count", 1, "number of HTTP 503 responses before recovery")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() > 0 || mock.FailCount < 0 {
		return errors.New("unexpected arguments or negative fail-count")
	}
	if *fixtures != "" {
		mock.Files = os.DirFS(*fixtures)
	}
	server := &http.Server{Addr: *addr, Handler: mock, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	fmt.Fprintln(out, "Fictional Northstar supplier listening on http://"+*addr)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
