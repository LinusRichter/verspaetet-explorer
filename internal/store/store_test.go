package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/LinusRichter/verspaetet-explorer/internal/dataset"
)

type stationRow struct {
	Eva      string `parquet:"eva"`
	Name     string `parquet:"name"`
	Category *int32 `parquet:"category,optional"`
}

func writeParquet[T any](t *testing.T, path string, rows []T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := parquet.Write(f, rows); err != nil {
		t.Fatal(err)
	}
}

func TestStopEventsReadsOnlyTheWindow(t *testing.T) {
	dir := t.TempDir()
	writeParquet(t, filepath.Join(dir, dataset.StationsFile), []stationRow{{Eva: "8000105", Name: "Frankfurt(Main)Hbf"}})
	ts := func(s string) int64 {
		tt, _ := time.Parse(time.RFC3339, s)
		return tt.Unix()
	}
	for day, at := range map[string]string{"2026-10-14": "2026-10-14T10:00:00Z", "2026-10-15": "2026-10-15T10:00:00Z", "2026-10-20": "2026-10-20T10:00:00Z"} {
		writeParquet(t, filepath.Join(dir, dataset.DaysDir, day, "2026-10.parquet"), []dataset.StopEvent{{
			StationEva: "8000105", Direction: "departure", StopID: "x-" + day + "-1",
			PlannedTime: ts(at), ScrapedAt: ts(at), ViaPath: "A|B",
		}})
	}

	// Only days of prepared months are offered.
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"months":{"2026-10":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeParquet(t, filepath.Join(dir, dataset.DaysDir, "2026-11-01", "2026-10.parquet"), []dataset.StopEvent{{StopID: "y-1", ScrapedAt: ts("2026-10-31T23:30:00Z")}})

	ctx := context.Background()
	st, err := Open(ctx, dir, Options{Threads: 1, MemoryLimit: "256MB"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()

	if got := st.Days(); len(got) != 3 || got[0] != "2026-10-14" || got[2] != "2026-10-20" {
		t.Fatalf("Days = %v", got)
	}
	from, _ := time.Parse(time.RFC3339, "2026-10-14T00:00:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-10-15T12:00:00Z")
	var n int
	var latest time.Time
	q := "SELECT count(*), max(scraped_at) FROM " + st.StopEvents(from, to) + " se JOIN stations s ON s.eva = se.station_eva"
	if err := st.DB.QueryRowContext(ctx, q).Scan(&n, &latest); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rows in window = %d, want 2 (14 and 15 Oct, not 20 Oct)", n)
	}
	if !latest.Equal(time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("max(scraped_at) = %v", latest)
	}

	far, _ := time.Parse(time.RFC3339, "2027-01-01T00:00:00Z")
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+st.StopEvents(far, far)).Scan(&n); err != nil || n != 0 {
		t.Errorf("empty window: n=%d err=%v", n, err)
	}
}
