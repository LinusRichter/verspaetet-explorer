// Package store answers queries over the prepared day files with an embedded,
// in-memory DuckDB. Nothing is loaded up front: every query reads only the
// day files its time window touches.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2" // registers the "duckdb" driver

	"github.com/LinusRichter/verspaetet-explorer/internal/dataset"
)

// Store is safe for concurrent use.
type Store struct {
	DB      *sql.DB
	dataDir string

	mu   sync.RWMutex
	days map[string][]string // YYYY-MM-DD -> day files (one per source month)
}

// Options bounds DuckDB so the explorer never takes over the machine.
type Options struct {
	Threads     int    // DuckDB worker threads
	MemoryLimit string // e.g. "1GB"
}

// Open opens an in-memory DuckDB and registers the prepared data.
func Open(ctx context.Context, dataDir string, opt Options) (*Store, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if opt.Threads <= 0 {
		opt.Threads = 2
	}
	if opt.MemoryLimit == "" {
		opt.MemoryLimit = "800MB"
	}
	for _, q := range []string{
		fmt.Sprintf("SET threads = %d", opt.Threads),
		fmt.Sprintf("SET memory_limit = '%s'", strings.ReplaceAll(opt.MemoryLimit, "'", "")),
		"SET enable_progress_bar = false",
		"SET TimeZone = 'UTC'",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			db.Close()
			return nil, fmt.Errorf("duckdb %q: %w", q, err)
		}
	}
	stations := filepath.ToSlash(filepath.Join(dataDir, dataset.StationsFile))
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE stations AS
		SELECT eva, name, category, false AS no_iris FROM read_parquet(%s)`, quote(stations))); err != nil {
		db.Close()
		return nil, fmt.Errorf("load stations: %w", err)
	}
	s := &Store{DB: db, dataDir: dataDir}
	if err := s.Rescan(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Rescan re-reads the list of prepared day files.
func (s *Store) Rescan() error {
	root := filepath.Join(s.dataDir, dataset.DaysDir)
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	days := map[string][]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(root, e.Name(), "*.parquet"))
		for _, f := range files {
			days[e.Name()] = append(days[e.Name()], filepath.ToSlash(f))
		}
	}
	s.mu.Lock()
	s.days = days
	s.mu.Unlock()
	return nil
}

// Days returns the days that can be explored, oldest first: the calendar
// days of the prepared months. A day at the edge of a month also has a few
// snapshots in the neighbouring month's file (trips are published by their
// operating day), but it only counts as available when its own month is
// prepared, so the UI never offers a half-covered day.
func (s *Store) Days() []string {
	months := map[string]bool{}
	for _, m := range dataset.PreparedMonths(s.dataDir) {
		months[m] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.days))
	for d := range s.days {
		if months[d[:7]] {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// StopEvents returns a SQL relation (usable after FROM) with the stop_events
// snapshots observed on the Berlin days from the day of `from` through the
// day of `to` (whole days; filter further in SQL). Columns match the
// published stop_events.parquet; planned_time, actual_time and scraped_at
// are TIMESTAMPTZ, via_path is the pipe-separated route string.
func (s *Store) StopEvents(from, to time.Time) string {
	s.mu.RLock()
	var files []string
	for d := dayOf(from); d <= dayOf(to); d = nextDay(d) {
		files = append(files, s.days[d]...)
	}
	s.mu.RUnlock()
	if len(files) == 0 {
		return emptyStopEvents
	}
	q := make([]string, len(files))
	for i, f := range files {
		q[i] = quote(f)
	}
	return fmt.Sprintf(`(SELECT station_eva, direction, stop_id, trip_date, line_category,
		line_label, train_number, owner, trip_kind, direction_name, via_path,
		to_timestamp(planned_time) AS planned_time, to_timestamp(actual_time) AS actual_time,
		planned_platform, platform, cancelled, to_timestamp(scraped_at) AS scraped_at
		FROM read_parquet([%s], union_by_name = true))`, strings.Join(q, ", "))
}

const emptyStopEvents = `(SELECT NULL::VARCHAR AS station_eva, NULL::VARCHAR AS direction, NULL::VARCHAR AS stop_id,
	NULL::VARCHAR AS trip_date, NULL::VARCHAR AS line_category, NULL::VARCHAR AS line_label,
	NULL::VARCHAR AS train_number, NULL::VARCHAR AS owner, NULL::VARCHAR AS trip_kind,
	NULL::VARCHAR AS direction_name, NULL::VARCHAR AS via_path, NULL::TIMESTAMPTZ AS planned_time,
	NULL::TIMESTAMPTZ AS actual_time, NULL::VARCHAR AS planned_platform, NULL::VARCHAR AS platform,
	NULL::BOOLEAN AS cancelled, NULL::TIMESTAMPTZ AS scraped_at WHERE false)`

func dayOf(t time.Time) string { return t.In(dataset.Berlin).Format("2006-01-02") }

func nextDay(d string) string {
	t, _ := time.ParseInLocation("2006-01-02", d, dataset.Berlin)
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
