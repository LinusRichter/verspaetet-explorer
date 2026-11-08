package dataset

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

func TestSelectMonths(t *testing.T) {
	pub := []string{"2026-09", "2026-10", "2026-11"}
	cases := []struct {
		spec    string
		want    []string
		wantErr bool
	}{
		{"", []string{"2026-11"}, false},
		{"latest", []string{"2026-11"}, false},
		{"ALL", pub, false},
		{"2026-09, 2026-10", []string{"2026-09", "2026-10"}, false},
		{"2026-08", nil, true},
		{",", nil, true},
	}
	for _, c := range cases {
		got, err := SelectMonths(c.spec, pub)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err = %v", c.spec, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v, want %v", c.spec, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: got %v, want %v", c.spec, got, c.want)
			}
		}
	}
	if _, err := SelectMonths("latest", nil); err == nil {
		t.Error("no published months must be an error")
	}
}

func TestDayOfUsesBerlinTime(t *testing.T) {
	// 22:30 UTC on 14 Oct is 00:30 on 15 Oct in Berlin (CEST).
	ts := time.Date(2026, 10, 14, 22, 30, 0, 0, time.UTC).Unix()
	if got := DayOf(ts); got != "2026-10-15" {
		t.Errorf("DayOf = %s, want 2026-10-15", got)
	}
}

func TestSplitByDay(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "stop_events.parquet")
	at := func(s string) int64 {
		tt, _ := time.Parse(time.RFC3339, s)
		return tt.Unix()
	}
	rows := []StopEvent{
		{StopID: "a-1", StationEva: "1", ScrapedAt: at("2026-10-14T10:00:00Z")},
		{StopID: "a-2", StationEva: "2", ScrapedAt: at("2026-10-14T23:00:00Z")}, // Berlin: 15 Oct
		{StopID: "b-1", StationEva: "1", ScrapedAt: at("2026-10-15T12:00:00Z")},
	}
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := parquet.Write(f, rows); err != nil {
		t.Fatal(err)
	}
	f.Close()

	n, days, err := SplitByDay(src, filepath.Join(dir, "days"), "2026-10")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || days != 2 {
		t.Fatalf("rows=%d days=%d, want 3 and 2", n, days)
	}
	got, err := parquet.ReadFile[StopEvent](filepath.Join(dir, "days", "2026-10-15", "2026-10.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("15 Oct has %d rows, want 2", len(got))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "days", "*", "*.part")); len(left) != 0 {
		t.Errorf("leftover temp files: %v", left)
	}
}
