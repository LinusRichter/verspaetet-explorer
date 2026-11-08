package dataset

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Layout of the data directory:
//
//	<data>/stations.parquet              station registry
//	<data>/months/<YYYY-MM>/manifest.json
//	<data>/months/<YYYY-MM>/scrape_runs.parquet
//	<data>/days/<YYYY-MM-DD>/<YYYY-MM>.parquet   stop_events of one day
//	<data>/state.json                    which months are prepared
const (
	StationsFile = "stations.parquet"
	MonthsDir    = "months"
	DaysDir      = "days"
	stateFile    = "state.json"
)

type monthState struct {
	StopEventsSha string    `json:"stop_events_sha256"`
	Rows          int64     `json:"rows"`
	Days          int       `json:"days"`
	PreparedAt    time.Time `json:"prepared_at"`
}

type state struct {
	Months map[string]monthState `json:"months"`
}

func loadState(dataDir string) state {
	st := state{Months: map[string]monthState{}}
	b, err := os.ReadFile(filepath.Join(dataDir, stateFile))
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Months == nil {
		st.Months = map[string]monthState{}
	}
	return st
}

func saveState(dataDir string, st state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dataDir, stateFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, stateFile))
}

// SelectMonths resolves the MONTHS setting against the published months:
// "latest" (default), "all", or a comma-separated list of YYYY-MM.
func SelectMonths(spec string, published []string) ([]string, error) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if len(published) == 0 {
		return nil, fmt.Errorf("the dataset has no published months yet")
	}
	switch spec {
	case "", "latest":
		return published[len(published)-1:], nil
	case "all":
		return published, nil
	}
	have := map[string]bool{}
	for _, m := range published {
		have[m] = true
	}
	var out []string
	for _, m := range strings.Split(spec, ",") {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !have[m] {
			return nil, fmt.Errorf("month %q is not published (available: %s)", m, strings.Join(published, ", "))
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("MONTHS=%q selects nothing", spec)
	}
	return out, nil
}

// Sync makes sure every wanted month is downloaded, verified and split into
// day files. Months that are already prepared with the same published
// checksum are skipped, so a restart costs nothing.
func Sync(ctx context.Context, src Source, dataDir, monthsSpec string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	published, err := src.Months(ctx)
	if err != nil {
		return err
	}
	wanted, err := SelectMonths(monthsSpec, published)
	if err != nil {
		return err
	}
	log.Printf("dataset %s: published %s; selected %s", src.Repo, strings.Join(published, ", "), strings.Join(wanted, ", "))

	st := loadState(dataDir)
	var latest *Manifest
	for _, month := range wanted {
		man, err := src.Manifest(ctx, month)
		if err != nil {
			return err
		}
		latest = man
		if cur, ok := st.Months[month]; ok && cur.StopEventsSha == man.StopEvents.Sha256 {
			log.Printf("%s: ready (%d rows, %d days)", month, cur.Rows, cur.Days)
			continue
		}
		if err := prepareMonth(ctx, src, dataDir, month, man); err != nil {
			return err
		}
		rows, days, err := splitMonth(dataDir, month)
		if err != nil {
			return err
		}
		if rows != man.StopEvents.Rows {
			return fmt.Errorf("%s: split %d rows, manifest says %d", month, rows, man.StopEvents.Rows)
		}
		st.Months[month] = monthState{StopEventsSha: man.StopEvents.Sha256, Rows: rows, Days: days, PreparedAt: time.Now().UTC()}
		if err := saveState(dataDir, st); err != nil {
			return err
		}
		// The month file is no longer needed once it is split into days.
		os.Remove(filepath.Join(dataDir, MonthsDir, month, "stop_events.parquet"))
		log.Printf("%s: ready (%d rows, %d days)", month, rows, days)
	}

	// The station registry is refreshed with every export; take the current one.
	stPath := filepath.Join(dataDir, StationsFile)
	if _, err := os.Stat(stPath); err != nil || needsRefresh(stPath) {
		want := ""
		if latest != nil && latest.Year > 0 && wanted[len(wanted)-1] == published[len(published)-1] {
			want = latest.Stations.Sha256
		}
		if err := src.Download(ctx, StationsFile, stPath, want, 0); err != nil {
			// A newer export may have replaced stations.parquet meanwhile; retry unverified.
			log.Printf("stations: %v; retrying without checksum", err)
			if err := src.Download(ctx, StationsFile, stPath, "", 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// PreparedMonths lists the months that are fully prepared, oldest first.
func PreparedMonths(dataDir string) []string {
	st := loadState(dataDir)
	out := make([]string, 0, len(st.Months))
	for m := range st.Months {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func needsRefresh(path string) bool {
	fi, err := os.Stat(path)
	return err != nil || time.Since(fi.ModTime()) > 7*24*time.Hour
}

func prepareMonth(ctx context.Context, src Source, dataDir, month string, man *Manifest) error {
	dir := filepath.Join(dataDir, MonthsDir, month)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(man, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644); err != nil {
		return err
	}
	log.Printf("%s: downloading stop_events.parquet (%d MB, %d rows)", month, man.StopEvents.SizeBytes>>20, man.StopEvents.Rows)
	if err := src.Download(ctx, month+"/stop_events.parquet", filepath.Join(dir, "stop_events.parquet"),
		man.StopEvents.Sha256, man.StopEvents.SizeBytes); err != nil {
		return err
	}
	if man.ScrapeRuns.Sha256 != "" {
		if err := src.Download(ctx, month+"/scrape_runs.parquet", filepath.Join(dir, "scrape_runs.parquet"),
			man.ScrapeRuns.Sha256, man.ScrapeRuns.SizeBytes); err != nil {
			return err
		}
	}
	return nil
}

func splitMonth(dataDir, month string) (int64, int, error) {
	daysDir := filepath.Join(dataDir, DaysDir)
	// Drop day files of an earlier version of this month.
	entries, _ := os.ReadDir(daysDir)
	for _, e := range entries {
		os.Remove(filepath.Join(daysDir, e.Name(), month+".parquet"))
	}
	log.Printf("%s: splitting into day files (one pass, takes a few minutes)", month)
	t := time.Now()
	rows, days, err := SplitByDay(filepath.Join(dataDir, MonthsDir, month, "stop_events.parquet"), daysDir, month)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", month, err)
	}
	log.Printf("%s: %d rows into %d day files in %s", month, rows, days, time.Since(t).Round(time.Second))
	return rows, days, nil
}
