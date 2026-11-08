package api

// Unit tests for the pure aggregation functions of the data UI
// (buildJourney / buildStop / buildBoard / buildLine) — no database.

import (
	"database/sql"
	"testing"
	"time"
)

func ts(day time.Time, min int) time.Time { return day.Add(time.Duration(min) * time.Minute) }

func berlinDay(dayStr string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", dayStr, berlinLoc)
	return t
}

// typical stop: 3 forecasts + actual — convergence +7 → +9 → +8 → actual +9
func mkSnapRows(day time.Time, plannedMin int, scrapes []int, progs []int, istAt int) []rawRow {
	rows := []rawRow{}
	for i, s := range scrapes {
		row := rawRow{
			StopID:   "123-2610170800-3",
			Planned:  ts(day, plannedMin),
			Scraped:  ts(day, s),
			Platform: "7",
			PlatFrom: "7",
			Cat:      "RE",
			Label:    "7",
		}
		if s >= istAt {
			a := ts(day, plannedMin+9)
			row.Actual = &a
		} else if i < len(progs) && progs[i] >= 0 {
			a := ts(day, plannedMin+progs[i])
			row.Actual = &a
		}
		rows = append(rows, row)
	}
	return rows
}

func TestBuildStopConvergence(t *testing.T) {
	day := berlinDay("2026-10-17")
	rows := mkSnapRows(day, 600, []int{410, 500, 550, 615}, []int{7, 9, 8}, 609)
	st := buildStop("123-2610170800-3", rows)

	if st.Idx != 3 {
		t.Errorf("Idx = %d, want 3", st.Idx)
	}
	if st.FinalD != 9 {
		t.Errorf("FinalD = %d, want 9 (the last actual row counts)", st.FinalD)
	}
	if st.Cancelled {
		t.Error("must not be cancelled")
	}
	if len(st.Snaps) != 4 {
		t.Fatalf("Snaps = %d, want 4", len(st.Snaps))
	}
	if !st.Snaps[len(st.Snaps)-1].Ist {
		t.Error("the last snapshot must be an actual")
	}
	if st.Snaps[0].Prog == nil || *st.Snaps[0].Prog != 7 {
		t.Errorf("first forecast = %v, want 7", st.Snaps[0].Prog)
	}
	if st.PlatKnown != 1<<30 {
		t.Errorf("PlatKnown = %d, want 'no change'", st.PlatKnown)
	}
}

func TestBuildStopPlatformChange(t *testing.T) {
	day := berlinDay("2026-10-17")
	rows := []rawRow{
		{StopID: "1-2610170800-1", Planned: ts(day, 600), Scraped: ts(day, 400), Platform: "3", PlatFrom: "3"},
		{StopID: "1-2610170800-1", Planned: ts(day, 600), Scraped: ts(day, 500), Platform: "5", PlatFrom: "3"},
		{StopID: "1-2610170800-1", Planned: ts(day, 600), Scraped: ts(day, 600), Platform: "5", PlatFrom: "3"},
	}
	st := buildStop("1-2610170800-1", rows)
	if st.PlatFrom != "3" || st.PlatTo != "5" {
		t.Errorf("platform %s → %s, want 3 → 5", st.PlatFrom, st.PlatTo)
	}
	if st.PlatKnown != 1 {
		t.Errorf("PlatKnown = %d, want 1 (change from the 2nd snapshot)", st.PlatKnown)
	}
}

func TestBuildStopCancelled(t *testing.T) {
	day := berlinDay("2026-10-17")
	rows := []rawRow{
		{StopID: "1-2610170812-2", Planned: ts(day, 492), Scraped: ts(day, 400), Platform: "2", PlatFrom: "2"},
		{StopID: "1-2610170812-2", Planned: ts(day, 492), Scraped: ts(day, 450), Platform: "2", PlatFrom: "2", Cancelled: true},
	}
	st := buildStop("1-2610170812-2", rows)
	if !st.Cancelled {
		t.Error("the stop must count as cancelled")
	}
	if st.FinalD != 0 {
		t.Errorf("FinalD = %d, want 0 (no actual time)", st.FinalD)
	}
}

func TestBuildJourneyCrossMidnight(t *testing.T) {
	// An ICE departs 23:54, the last stop is past midnight — the anchor must
	// be the trip's start day; the post-midnight stop gets minutes > 1440.
	day := berlinDay("2026-10-17")
	raw := []rawRow{
		// stop 1 (start): departure 23:54, actual +6
		{StopID: "9-2610172354-1", Direction: "departure", Name: "A", Eva: "1",
			Planned: ts(day, 23*60+54), Scraped: ts(day, 23*60+20),
			Actual: strpTime(day, 23*60+54+6), Platform: "1", PlatFrom: "1", Cat: "ICE", Label: "1021"},
		// stop 2: arrival 00:12 the next day, actual +6
		{StopID: "9-2610172354-2", Direction: "arrival", Name: "B", Eva: "2",
			Planned: ts(day, 24*60+12), Scraped: ts(day, 23*60+50),
			Actual: strpTime(day, 24*60+12+6), Platform: "4", PlatFrom: "4"},
	}
	run, line, ok := buildJourney("9-2610172354", raw)
	if !ok {
		t.Fatal("run must be found")
	}
	if line.Key != "ICE|1021" || line.Label != "ICE 1021" {
		t.Errorf("line = %+v", line)
	}
	if len(run.Stops) != 2 {
		t.Fatalf("Stops = %d, want 2", len(run.Stops))
	}
	if run.Dep != 23*60+54 {
		t.Errorf("Dep = %d, want 23:54 (=%d)", run.Dep, 23*60+54)
	}
	last := run.Stops[1]
	if last.Planned != 24*60+12 {
		t.Errorf("post-midnight stop: Planned = %d, want 1452 (>1440, consistent anchor)", last.Planned)
	}
	if last.Actual != 24*60+12+6 {
		t.Errorf("Actual = %d, want %d", last.Actual, 24*60+12+6)
	}
}

func strpTime(day time.Time, min int) *time.Time {
	t := ts(day, min)
	return &t
}

func TestBuildJourneyDedupArrDep(t *testing.T) {
	// one stop with arrival AND departure rows: departure wins for planned
	day := berlinDay("2026-10-17")
	raw := []rawRow{
		{StopID: "7-2610170900-1", Direction: "arrival", Name: "X", Eva: "1",
			Planned: ts(day, 540), Scraped: ts(day, 500), Platform: "2", PlatFrom: "2"},
		{StopID: "7-2610170900-1", Direction: "departure", Name: "X", Eva: "1",
			Planned: ts(day, 542), Scraped: ts(day, 500), Platform: "2", PlatFrom: "2", Cat: "RE", Label: "7"},
	}
	run, _, ok := buildJourney("7-2610170900", raw)
	if !ok || len(run.Stops) != 1 {
		t.Fatalf("one stop expected, ok=%v stops=%d", ok, len(run.Stops))
	}
	if run.Stops[0].Planned != 542 {
		t.Errorf("Planned = %d, want 542 (departure)", run.Stops[0].Planned)
	}
}

func TestBuildBoardWindowOrder(t *testing.T) {
	day := berlinDay("2026-10-17")
	anchor := day
	raw := []rawRow{
		{StopID: "5-2610171000-2", Name: "Köln Hbf", Cat: "S", Label: "6",
			Planned: ts(day, 610), Scraped: ts(day, 600), Platform: "8", PlatFrom: "8", Via: []string{"Troisdorf", "Siegburg"}, Dest: "Siegburg/Bonn"},
		{StopID: "6-2610170900-1", Name: "Köln Hbf", Cat: "RE", Label: "7",
			Planned: ts(day, 540), Scraped: ts(day, 500), Platform: "3", PlatFrom: "5", Dest: "Krefeld"},
	}
	board := buildBoard(raw, anchor)
	if len(board.Rows) != 2 {
		t.Fatalf("Rows = %d, want 2", len(board.Rows))
	}
	if board.Rows[0].RunID != "6-2610170900" {
		t.Errorf("order by planned time: first is %s", board.Rows[0].RunID)
	}
	r := board.Rows[0]
	if !r.PlatChanged {
		t.Error("platform change 5 → 3 not detected")
	}
	if r.LineKey != "RE|7" || r.LineLabel != "RE 7" {
		t.Errorf("LineKey/Label = %s/%s", r.LineKey, r.LineLabel)
	}
	if len(board.Lines) != 2 {
		t.Errorf("Lines = %d, want 2 (both lines of the station)", len(board.Lines))
	}
}

func TestBuildLineAggregation(t *testing.T) {
	day := berlinDay("2026-10-17")
	anchor := day
	mk := func(trip string, idx int, name string, plannedMin int, delayMin int) rawRow {
		row := rawRow{StopID: trip + "-" + itoa(idx), Direction: "departure", Name: name,
			Planned: ts(day, plannedMin), Scraped: ts(day, plannedMin-10), Cat: "RE", Label: "7"}
		if delayMin > 0 {
			row.Actual = strpTime(day, plannedMin+delayMin)
		}
		return row
	}
	raw := []rawRow{
		// run 1: 3 stops, end delay +12
		mk("t1", 1, "A", 500, 2), mk("t1", 2, "B", 520, 8), mk("t1", 3, "C", 540, 12),
		// run 2: +3
		mk("t2", 1, "A", 600, 1), mk("t2", 2, "B", 620, 2), mk("t2", 3, "C", 640, 3),
		// run 3: cancelled (last stop cancelled)
		mk("t3", 1, "A", 700, 0), mk("t3", 2, "B", 720, 0),
	}
	raw[4].Cancelled = false
	raw[len(raw)-1].Cancelled = true

	out := buildLine(raw, anchor, "RE|7", "RE", "7")
	if len(out.Runs) != 3 {
		t.Fatalf("Runs = %d, want 3", len(out.Runs))
	}
	if out.Stats.N != 3 || out.Stats.Cx != 1 {
		t.Errorf("Stats n=%d cx=%d, want 3/1", out.Stats.N, out.Stats.Cx)
	}
	if out.Stats.P90 != 12 {
		t.Errorf("P90 = %d, want 12", out.Stats.P90)
	}
	if out.Stats.Punc != 50 { // non-cancelled runs: 2, of which punctual 1 (+3) → 50
		t.Errorf("Punc = %d, want 50", out.Stats.Punc)
	}
	if out.Runs[0].FinalD != 12 || out.Runs[1].FinalD != 3 {
		t.Errorf("finalD per run: %d, %d — want 12, 3", out.Runs[0].FinalD, out.Runs[1].FinalD)
	}
	if !out.Runs[2].Cancelled {
		t.Error("run 3 must be cancelled (last stop cancelled)")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestTripKeyAndStopIdx(t *testing.T) {
	cases := map[string][2]any{
		"-479827450801391963-2610161150-6":  {"-479827450801391963-2610161150", 6},
		"3371964485750035452-2609221621-15": {"3371964485750035452-2609221621", 15},
	}
	for stopID, exp := range cases {
		if tripKey(stopID) != exp[0].(string) {
			t.Errorf("tripKey(%s) = %s, want %s", stopID, tripKey(stopID), exp[0])
		}
		if stopIdxOf(stopID) != exp[1].(int) {
			t.Errorf("stopIdxOf(%s) = %d, want %d", stopID, stopIdxOf(stopID), exp[1])
		}
	}
}

func TestRunIDAndSplitVia(t *testing.T) {
	if !runIDRe.MatchString("-1991843947888629672-2610270809") || runIDRe.MatchString("abc-1") {
		t.Error("run id format")
	}
	// via_path arrives pipe-separated from the day files
	if got := splitVia(sql.NullString{String: "A|B|C", Valid: true}); len(got) != 3 || got[2] != "C" {
		t.Errorf("splitVia = %v", got)
	}
	if got := splitVia(sql.NullString{String: "", Valid: true}); got != nil {
		t.Errorf("empty via_path must be nil, got %v", got)
	}
	if got := splitVia(sql.NullString{}); got != nil {
		t.Errorf("NULL via_path must be nil, got %v", got)
	}
}
