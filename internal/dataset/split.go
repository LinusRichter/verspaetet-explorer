package dataset

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

// StopEvent mirrors one row of the published stop_events.parquet.
type StopEvent struct {
	StationEva      string  `parquet:"station_eva"`
	Direction       string  `parquet:"direction"`
	StopID          string  `parquet:"stop_id"`
	TripDate        string  `parquet:"trip_date"`
	LineCategory    string  `parquet:"line_category"`
	LineLabel       string  `parquet:"line_label,optional"`
	TrainNumber     string  `parquet:"train_number,optional"`
	Owner           string  `parquet:"owner,optional"`
	TripKind        string  `parquet:"trip_kind,optional"`
	DirectionName   string  `parquet:"direction_name,optional"`
	ViaPath         string  `parquet:"via_path,optional"` // pipe-separated
	PlannedTime     int64   `parquet:"planned_time"`      // unix seconds UTC
	ActualTime      *int64  `parquet:"actual_time,optional"`
	PlannedPlatform *string `parquet:"planned_platform,optional"`
	Platform        *string `parquet:"platform,optional"`
	Cancelled       bool    `parquet:"cancelled"`
	ScrapedAt       int64   `parquet:"scraped_at"` // unix seconds UTC
}

// Berlin is the time zone of the operating day.
var Berlin = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic("Europe/Berlin unavailable: " + err.Error())
	}
	return loc
}()

// DayOf returns the operating day (Europe/Berlin) a snapshot belongs to,
// by the time it was observed: the axis the explorer scrubs along.
func DayOf(scrapedAt int64) string {
	return time.Unix(scrapedAt, 0).In(Berlin).Format("2006-01-02")
}

// SplitByDay streams src once and writes one Parquet file per observed day
// to <daysDir>/<YYYY-MM-DD>/<tag>.parquet (tag = source month, because a
// day at a month boundary is covered by two published months). Memory stays
// bounded: rows are buffered per day in small batches and row groups are
// capped.
func SplitByDay(src, daysDir, tag string) (rows int64, days int, err error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	r := parquet.NewGenericReader[StopEvent](f)
	defer r.Close()
	total := r.NumRows()

	type sink struct {
		f   *os.File
		w   *parquet.GenericWriter[StopEvent]
		buf []StopEvent
		tmp string
		dst string
	}
	sinks := map[string]*sink{}
	cleanup := func() {
		for _, s := range sinks {
			s.f.Close()
			os.Remove(s.tmp)
		}
	}
	flush := func(s *sink) error {
		if len(s.buf) == 0 {
			return nil
		}
		_, err := s.w.Write(s.buf)
		s.buf = s.buf[:0]
		return err
	}

	batch := make([]StopEvent, 4096)
	next := time.Now().Add(15 * time.Second)
	for {
		n, rerr := r.Read(batch)
		for _, row := range batch[:n] {
			day := DayOf(row.ScrapedAt)
			s := sinks[day]
			if s == nil {
				dir := filepath.Join(daysDir, day)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					cleanup()
					return 0, 0, err
				}
				dst := filepath.Join(dir, tag+".parquet")
				fh, err := os.Create(dst + ".part")
				if err != nil {
					cleanup()
					return 0, 0, err
				}
				s = &sink{f: fh, tmp: dst + ".part", dst: dst,
					w: parquet.NewGenericWriter[StopEvent](fh,
						parquet.Compression(&zstd.Codec{}),
						parquet.MaxRowsPerRowGroup(20_000))}
				sinks[day] = s
			}
			s.buf = append(s.buf, row)
			if len(s.buf) >= 1024 {
				if err := flush(s); err != nil {
					cleanup()
					return 0, 0, err
				}
			}
		}
		rows += int64(n)
		if time.Now().After(next) && total > 0 {
			log.Printf("  splitting %s into days: %d%%", tag, rows*100/total)
			next = time.Now().Add(15 * time.Second)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			cleanup()
			return 0, 0, fmt.Errorf("read %s: %w", src, rerr)
		}
	}
	for day, s := range sinks {
		if err := flush(s); err != nil {
			cleanup()
			return 0, 0, err
		}
		if err := s.w.Close(); err != nil {
			cleanup()
			return 0, 0, fmt.Errorf("write day %s: %w", day, err)
		}
		if err := s.f.Close(); err != nil {
			cleanup()
			return 0, 0, err
		}
	}
	for _, s := range sinks {
		if err := os.Rename(s.tmp, s.dst); err != nil {
			return 0, 0, err
		}
	}
	return rows, len(sinks), nil
}
