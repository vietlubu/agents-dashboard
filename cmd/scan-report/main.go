// Command scan-report runs one full scan of every harness and prints what it found.
//
// It exists so the ingest rules can be checked against real session data without the UI:
// the numbers it prints for each harness must match that harness's stored events and, for
// OpenCode and omp, the aggregates the harness itself records.
//
// It never touches the real database unless -db points at one: by default it scans into a
// throwaway file that is deleted on exit.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"text/tabwriter"
	"time"

	"github.com/vietlubu/agent-dashboard/internal/config"
	"github.com/vietlubu/agent-dashboard/internal/pricing"
	"github.com/vietlubu/agent-dashboard/internal/store"
	syncengine "github.com/vietlubu/agent-dashboard/internal/sync"
)

func main() {
	var (
		dbPath  = flag.String("db", "", "database path; empty means a temporary file that is removed on exit")
		asJSON  = flag.Bool("json", false, "print the report as JSON")
		home    = flag.String("home", "", "override the application data directory")
		quiet   = flag.Bool("quiet", false, "suppress per-harness progress output")
		memprof = flag.String("memprofile", "", "write a heap profile here")
	)
	flag.Parse()

	if *home != "" {
		_ = os.Setenv("AGENT_DASHBOARD_HOME", *home)
	}

	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}

	path := *dbPath
	cleanup := func() {}
	if path == "" {
		dir, err := os.MkdirTemp("", "agent-dashboard-scan")
		if err != nil {
			fatal(err)
		}
		path = filepath.Join(dir, "scan.db")
		cleanup = func() { _ = os.RemoveAll(dir) }
	}
	defer cleanup()

	db, err := store.Open(path)
	if err != nil {
		fatal(err)
	}
	defer db.Close()

	level := slog.LevelWarn
	if !*quiet {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	catalog := pricing.NewCatalog(logger)
	if err := catalog.Reload(context.Background(), db); err != nil {
		fatal(err)
	}

	lastLine := time.Time{}
	engine := syncengine.New(db, cfg, catalog, logger, func(name string, payload any) {
		if *asJSON || *quiet || name != syncengine.EventProgress {
			return
		}
		p, ok := payload.(syncengine.ProgressEvent)
		if !ok {
			return
		}
		if time.Since(lastLine) < 400*time.Millisecond {
			return
		}
		lastLine = time.Now()
		fmt.Fprintf(os.Stderr, "  %-9s %-6s walked=%-5d changed=%-5d events=%-6d %dms\n",
			p.Harness, p.Phase, p.Walked, p.Changed, p.Events, p.ElapsedMs)
	})

	ctx := context.Background()
	summary, err := engine.Run(ctx, "manual")
	if err != nil {
		fatal(err)
	}

	report, err := engine.Report(ctx)
	if err != nil {
		fatal(err)
	}

	if *asJSON {
		out, err := json.MarshalIndent(map[string]any{
			"database": path,
			"summary":  summary,
			"report":   report,
		}, "", "  ")
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(out))
		return
	}

	fmt.Printf("database: %s\n", path)
	fmt.Printf("scan: %d walked, %d changed, %d inserted, %d updated in %dms\n\n",
		summary.FilesWalked, summary.FilesChanged, summary.EventsInserted,
		summary.EventsUpdated, summary.DurationMs)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "harness\tavail\troots\tsessions\tevents\tinput\toutput\tcache_read\tcache_write\treasoning\ttotal\tcost_usd\tlatency\tttft")
	for _, r := range report {
		avail := "no"
		if r.Available {
			avail = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.6f\t%d\t%d\n",
			r.ID, avail, len(r.Roots), r.Sessions, r.Events, r.Input, r.Output,
			r.CacheRead, r.CacheWrite, r.Reasoning, r.Total, r.CostUSD, r.LatencyRows, r.TTFTRows)
	}
	_ = w.Flush()

	var events, total int64
	var cost float64
	for _, r := range report {
		events += r.Events
		total += r.Total
		cost += r.CostUSD
	}
	fmt.Printf("\nTOTAL events=%d tokens=%d cost_usd=%.6f\n", events, total, cost)

	if *memprof != "" {
		f, err := os.Create(*memprof)
		if err != nil {
			fatal(err)
		}
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			fatal(err)
		}
		f.Close()
		fmt.Fprintf(os.Stderr, "heap profile written to %s\n", *memprof)
	}

	stats, err := engine.Stats(ctx)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("stored: %d events, %d sessions, %d bytes\n", stats.Events, stats.Sessions, stats.DBBytes)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "scan-report:", err)
	os.Exit(1)
}
