// Package api serves the explorer UI endpoints on top of the prepared
// verspaetet day files. Everything is read-only: queries run on the
// embedded, in-memory DuckDB (store.Store), which reads only the day files
// its time window touches.
//
// Time model: every time a response reports is MINUTES since the Berlin
// midnight anchor of the selected day. Trips across midnight get values
// > 1440 on the same timeline as the knowledge slider.
//
// Terms: run = stop_id prefix (dailyTripId-YYMMddHHmm), stop = stop_id
// group (arrival + departure at one station), snapshot = one stop_events
// row (scraped_at = knowledge time).
//
// The dataset is historical and immutable: caches never expire, they are
// only bounded by size, and HTTP responses carry long max-age.

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Europe/Berlin even in containers without OS tzdata

	"github.com/LinusRichter/verspaetet-explorer/internal/store"
)

// ---------------------------------------------------------------------------
// Time helpers
// ---------------------------------------------------------------------------

var berlinLoc = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic("Europe/Berlin unavailable despite embedded tzdata: " + err.Error())
	}
	return loc
}()

// dayAnchor returns midnight Europe/Berlin of the given timestamp's local day.
func dayAnchor(ts time.Time) time.Time {
	local := ts.In(berlinLoc)
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, berlinLoc)
}

// minutesOfDay: minutes since anchor; may be negative or > 1440.
func minutesOfDay(ts time.Time, anchor time.Time) int {
	return int(ts.Sub(anchor) / time.Minute)
}

// ---------------------------------------------------------------------------
// Bounded in-process cache: entries never expire (immutable data), but a
// size cap evicts the least recently used entries so arbitrary query
// parameters cannot grow memory without bound.
// ---------------------------------------------------------------------------

type cacheEntry[T any] struct {
	value T
	used  time.Time
}

type boundedCache[T any] struct {
	mu  sync.Mutex
	m   map[string]*cacheEntry[T]
	max int
}

func newBoundedCache[T any](max int) *boundedCache[T] {
	return &boundedCache[T]{m: make(map[string]*cacheEntry[T]), max: max}
}

func (c *boundedCache[T]) get(key string, now time.Time) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		var zero T
		return zero, false
	}
	e.used = now
	return e.value, true
}

// put stores v, evicting LRU entries when the cap is reached.
func (c *boundedCache[T]) put(key string, v T, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[key]; !exists && len(c.m) >= c.max {
		for len(c.m) >= c.max {
			lruKey, lruAt := "", time.Time{}
			for k, old := range c.m {
				if lruKey == "" || old.used.Before(lruAt) {
					lruKey, lruAt = k, old.used
				}
			}
			delete(c.m, lruKey)
		}
	}
	c.m[key] = &cacheEntry[T]{value: v, used: now}
}

func (c *boundedCache[T]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// ---------------------------------------------------------------------------
// Singleflight: concurrent calls with the same key share ONE computation.
// The computation runs with its own (detached) context — if the first
// client disconnects, the others still get the result (and the cache is
// filled for the next attempt).
// ---------------------------------------------------------------------------

type sfGroup[T any] struct {
	mu    sync.Mutex
	calls map[string]*sfCall[T]
}

type sfCall[T any] struct {
	done chan struct{}
	val  T
	err  error
}

// Do runs fn at most once concurrently per key. Waiters give up when their
// own ctx is cancelled; the computation itself keeps running.
func (g *sfGroup[T]) Do(ctx context.Context, key string, fn func() (T, error)) (T, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*sfCall[T])
	}
	c, exists := g.calls[key]
	if !exists {
		c = &sfCall[T]{done: make(chan struct{})}
		g.calls[key] = c
		go func() {
			c.val, c.err = fn()
			close(c.done)
			g.mu.Lock()
			delete(g.calls, key)
			g.mu.Unlock()
		}()
	}
	g.mu.Unlock()
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// Heavy semaphore: at most ONE heavy query (day aggregate, line scan,
// alt-lines miss, search index build) runs at a time. DuckDB is capped at
// a few threads, so serialization keeps the explorer responsive.
// ---------------------------------------------------------------------------

var errBusy = errors.New("server busy with another heavy query")

const (
	heavyWait        = 5 * time.Second   // how long a request waits for the slot
	heavyDayTimeout  = 150 * time.Second // context budget for the day aggregate
	heavyLineTimeout = 60 * time.Second  // context budget for the line scan
)

func (d *dataUI) acquireHeavy(ctx context.Context, wait time.Duration) (func(), error) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case d.heavy <- struct{}{}:
		return func() { <-d.heavy }, nil
	case <-t.C:
		return nil, errBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeBusy: 503 + Retry-After when the heavy slot is taken.
func writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "10")
	writeError(w, http.StatusServiceUnavailable, "busy — please retry in a few seconds")
}

// writeQueryError: log the SQL error, send the client a generic message.
func writeQueryError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, errBusy) {
		writeBusy(w)
		return
	}
	log.Printf("[api] %s: %v", what, err)
	writeError(w, 500, what+" failed")
}

// HTTP cache ages: prepared days are immutable.
const (
	ageData = 7 * 86400 // board/journey/line/day/search results
	ageMeta = 3600      // meta (the day list changes only with new exports)
)

// ---------------------------------------------------------------------------
// Raw row model: handlers scan, pure functions aggregate.
// ---------------------------------------------------------------------------

type rawRow struct {
	StopID    string
	Direction string
	Eva       string
	Name      string
	Planned   time.Time
	Actual    *time.Time
	Cancelled bool
	Platform  string
	PlatFrom  string
	Scraped   time.Time
	Cat       string
	Label     string
	Via       []string
	Dest      string
}

var tripSuffixRe = regexp.MustCompile(`-[0-9]+$`)

func tripKey(stopID string) string { return tripSuffixRe.ReplaceAllString(stopID, "") }

func stopIdxOf(stopID string) int {
	i := strings.LastIndex(stopID, "-")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(stopID[i+1:])
	if err != nil {
		return 0
	}
	return n
}

func lineKeyOf(cat, label string) string { return cat + "|" + label }

func lineLabelOf(cat, label string) string {
	// IRIS sometimes ships the public label including the category ("S11") —
	// then don't double it ("S S11"), but "S 11".
	if len(label) > len(cat) && strings.EqualFold(label[:len(cat)], cat) {
		return cat + " " + label[len(cat):]
	}
	return strings.TrimSpace(cat + " " + label)
}

// labelExpr: SQL expression for the public line label (line_label, with the
// operational number as fallback for very old days).
const labelExpr = `COALESCE(NULLIF(line_label, ''), NULLIF(train_number, ''), '')`

// labelExprSE: the same expression, qualified for the alias se.
const labelExprSE = `COALESCE(NULLIF(se.line_label, ''), NULLIF(se.train_number, ''), '')`

// splitVia turns the pipe-separated route string into its parts
// (empty or NULL → nil).
func splitVia(s sql.NullString) []string {
	if !s.Valid || s.String == "" {
		return nil
	}
	parts := strings.Split(s.String, "|")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ---------------------------------------------------------------------------
// JSON contracts (mirror web/src/types.ts)
// ---------------------------------------------------------------------------

type uiMeta struct {
	Day  string   `json:"day"`  // selected day, ISO YYYY-MM-DD
	Days []string `json:"days"` // all available days, oldest first
	T0   int      `json:"t0"`
	T1   int      `json:"t1"`
	Now  int      `json:"now"` // end of the day's data: 1440
}

type uiLineInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cat   string `json:"cat"`
	Ctx   string `json:"ctx,omitempty"`
}

type uiLineAtStation struct {
	Line    uiLineInfo `json:"line"`
	Station string     `json:"station"`
}

type uiBoardData struct {
	Rows  []uiBoardRow `json:"rows"`
	Lines []uiLineInfo `json:"lines"`
}

type uiBoardRow struct {
	RunID       string `json:"runId"`
	StopIdx     int    `json:"stopIdx"`
	LineKey     string `json:"lineKey"`
	LineLabel   string `json:"lineLabel"`
	Planned     int    `json:"planned"`
	Known       bool   `json:"known"`
	Delay       *int   `json:"delay"`
	Ist         bool   `json:"ist"`
	Cancelled   bool   `json:"cancelled"`
	Plat        string `json:"plat"`
	PlatFrom    string `json:"platFrom"`
	PlatChanged bool   `json:"platChanged"`
	Dest        string `json:"dest"`
	Via         string `json:"via"`
}

type uiAltLine struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cat   string `json:"cat"`
	RunID string `json:"runId"`
	Dep   int    `json:"dep"`
}

type uiSnap struct {
	At        int    `json:"at"`
	Prog      *int   `json:"prog"`
	Ist       bool   `json:"ist"`
	Cancelled bool   `json:"cancelled"`
	Plat      string `json:"plat"`

	// internal timestamp for the anchor pass (not JSON):
	atTS time.Time `json:"-"`
}

type uiStop struct {
	Name      string   `json:"name"`
	Eva       string   `json:"eva"`
	Idx       int      `json:"idx"`
	Planned   int      `json:"planned"`
	FinalD    int      `json:"finalD"`
	Actual    int      `json:"actual"`
	Cancelled bool     `json:"cancelled"`
	PlatFrom  string   `json:"platFrom"`
	PlatTo    string   `json:"platTo"`
	PlatKnown int      `json:"platKnown"` // large value = no change
	Snaps     []uiSnap `json:"snaps"`

	plannedTS time.Time `json:"-"`
}

type uiRun struct {
	ID        string   `json:"id"`
	LineKey   string   `json:"lineKey"`
	Dep       int      `json:"dep"`
	Cancelled bool     `json:"cancelled"`
	Stops     []uiStop `json:"stops"`
}

type uiJourneyData struct {
	Run       uiRun       `json:"run"`
	Line      uiLineInfo  `json:"line"`
	AltLines  []uiAltLine `json:"altLines"`
	AltScoped bool        `json:"altScoped"`
}

type uiLineRunStop struct {
	Name      string `json:"name"`
	FinalD    int    `json:"finalD"`
	Cancelled bool   `json:"cancelled"`
}

type uiLineRun struct {
	ID        string          `json:"id"`
	Dep       int             `json:"dep"`
	Cancelled bool            `json:"cancelled"`
	FinalD    int             `json:"finalD"`
	Stops     []uiLineRunStop `json:"stops"`
}

type uiLineStats struct {
	N    int `json:"n"`
	Cx   int `json:"cx"`
	P50  int `json:"p50"`
	P90  int `json:"p90"`
	Max  int `json:"max"`
	Punc int `json:"punc"`
	GC   int `json:"gc"`
}

type uiLineData struct {
	Line      uiLineInfo  `json:"line"`
	Ctx       string      `json:"ctx,omitempty"`
	Runs      []uiLineRun `json:"runs"`
	Stats     uiLineStats `json:"stats"`
	AltLines  []uiAltLine `json:"altLines"`
	AltScoped bool        `json:"altScoped"`
}

type uiDayKpis struct {
	Runs  int     `json:"runs"`
	Punc  int     `json:"punc"`
	Avg   float64 `json:"avg"`
	Cx    int     `json:"cx"`
	GC    int     `json:"gc"`
	Snaps int     `json:"snaps"`
}

type uiHourBar struct {
	H   int     `json:"h"`
	Avg float64 `json:"avg"`
	N   int     `json:"n"`
}

type uiLeaderRow struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	N     int    `json:"n"`
	P50   int    `json:"p50"`
	P90   int    `json:"p90"`
	Max   int    `json:"max"`
	Punc  int    `json:"punc"`
	Cx    int    `json:"cx"`
}

type uiTopRow struct {
	RunID  string `json:"runId"`
	Label  string `json:"label"`
	Dep    int    `json:"dep"`
	From   string `json:"from"`
	To     string `json:"to"`
	FinalD int    `json:"finalD"`
}

type dayData struct {
	Kpis        uiDayKpis     `json:"kpis"`
	Hours       []uiHourBar   `json:"hours"`
	Leaderboard []uiLeaderRow `json:"leaderboard"`
	Top         []uiTopRow    `json:"top"`

	TripsOfDay []tripFact `json:"-"`
}

type tripFact struct {
	Trip      string
	Cat       string
	Label     string
	DepMin    int
	DepFrom   string
	EndTo     string
	FinalD    int
	Cancelled bool
}

// ---------------------------------------------------------------------------
// dataUI
// ---------------------------------------------------------------------------

type dataUI struct {
	st *store.Store

	dayCache  *boundedCache[dayData]     // key: YYYY-MM-DD
	stationLn *boundedCache[[]uiAltLine] // key: date|eva — lines at a station
	lineCache *boundedCache[[]rawRow]    // key: date|cat|label — line rows (unscoped)

	daySF  sfGroup[dayData]
	lineSF sfGroup[[]rawRow]

	heavy chan struct{} // capacity 1 — see acquireHeavy

	stations stationStore // station index (RAM), lazy with retry
	search   searchStore  // per-day search indexes (search.go)
}

var runIDRe = regexp.MustCompile(`^-?[0-9]+-[0-9]{10}$`)

// Register wires the explorer API onto mux.
func Register(mux *http.ServeMux, st *store.Store) {
	d := &dataUI{
		st:        st,
		dayCache:  newBoundedCache[dayData](40),
		stationLn: newBoundedCache[[]uiAltLine](512),
		lineCache: newBoundedCache[[]rawRow](64),
		heavy:     make(chan struct{}, 1),
	}
	d.stations.db = st.DB
	// No warm-up: nothing happens at startup — work only on request.
	mux.HandleFunc("GET /api/meta", d.handleMeta)
	mux.HandleFunc("GET /api/search", d.handleSearch)
	mux.HandleFunc("GET /api/board", d.handleBoard)
	mux.HandleFunc("GET /api/journey", d.handleJourney)
	mux.HandleFunc("GET /api/line", d.handleLine)
	mux.HandleFunc("GET /api/day", d.handleDay)
}

// parseDay resolves the day parameter against the available days: empty
// means the LATEST available day, anything else must be listed by
// st.Days(). Returns [start, end) and the Berlin midnight anchor.
func parseDay(dayStr string, days []string) (start, end, anchor time.Time, err error) {
	if len(days) == 0 {
		return time.Time{}, time.Time{}, time.Time{}, errors.New("no data prepared")
	}
	if dayStr == "" {
		dayStr = days[len(days)-1]
	}
	ok := false
	for _, d := range days {
		if d == dayStr {
			ok = true
			break
		}
	}
	if !ok {
		return time.Time{}, time.Time{}, time.Time{},
			fmt.Errorf("day %q not available (data covers %s to %s)", dayStr, days[0], days[len(days)-1])
	}
	t, perr := time.ParseInLocation("2006-01-02", dayStr, berlinLoc)
	if perr != nil {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("invalid day %q (YYYY-MM-DD)", dayStr)
	}
	return t, t.AddDate(0, 0, 1), t, nil
}

// dayWindow: parseDay for the request; writes the 400 itself on error.
func (d *dataUI) dayWindow(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, time.Time, bool) {
	start, end, anchor, err := parseDay(r.URL.Query().Get("day"), d.st.Days())
	if err != nil {
		writeError(w, 400, err.Error())
		return start, end, anchor, false
	}
	return start, end, anchor, true
}

// ---------------------------------------------------------------------------
// Station index: loaded lazily on first use, retried after failures (no
// sync.Once). The station list is immutable — no periodic refresh.
// ---------------------------------------------------------------------------

const stationRetry = 30 * time.Second

type stationStore struct {
	db *sql.DB

	mu         sync.Mutex
	idx        *stationIndex
	loading    bool
	lastFail   time.Time
	loadSignal chan struct{} // closed when a running load ends
}

// get returns the index (nil if it cannot be loaded).
func (s *stationStore) get(ctx context.Context) *stationIndex {
	s.mu.Lock()
	cur := s.idx
	if cur != nil {
		s.mu.Unlock()
		return cur
	}
	if !s.loading {
		if time.Since(s.lastFail) < stationRetry {
			s.mu.Unlock()
			return nil
		}
		s.startLoadLocked()
	}
	sig := s.loadSignal
	s.mu.Unlock()
	select {
	case <-sig:
	case <-ctx.Done():
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idx
}

func (s *stationStore) startLoadLocked() {
	s.loading = true
	s.loadSignal = make(chan struct{})
	sig := s.loadSignal
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		idx, err := loadStationIndex(ctx, s.db)
		s.mu.Lock()
		if err != nil {
			log.Printf("[api] stations load: %v", err)
			s.lastFail = time.Now()
		} else {
			s.idx = idx
			log.Printf("[api] stations index: %d entries", len(idx.list))
		}
		s.loading = false
		s.mu.Unlock()
		close(sig)
	}()
}

// set: only for tests (fixture index without a DB).
func (s *stationStore) set(idx *stationIndex) {
	s.mu.Lock()
	s.idx = idx
	s.mu.Unlock()
}

// resolveStation: station name → EVA from the RAM index. ok=false when the
// name is definitely unknown (then NO query). If the index is unavailable,
// no validation happens (eva="" → SQL resolves the name).
func (d *dataUI) resolveStation(ctx context.Context, name string) (eva string, ok bool) {
	idx := d.stations.get(ctx)
	if idx == nil {
		return "", true
	}
	i, found := idx.byName[name]
	if !found {
		return "", false
	}
	return idx.list[i].Eva, true
}

func (d *dataUI) handleMeta(w http.ResponseWriter, r *http.Request) {
	start, _, _, ok := d.dayWindow(w, r)
	if !ok {
		return
	}
	writeJSONCached(w, r, ageMeta, uiMeta{
		Day:  start.Format("2006-01-02"),
		Days: d.st.Days(),
		T0:   0,
		T1:   1440,
		Now:  1440,
	})
}

// ---------------------------------------------------------------------------
// Station board
// ---------------------------------------------------------------------------

func (d *dataUI) handleBoard(w http.ResponseWriter, r *http.Request) {
	station := strings.TrimSpace(r.URL.Query().Get("station"))
	atMin := 877
	if v := r.URL.Query().Get("at"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			atMin = n
		}
	}
	if station == "" {
		writeError(w, 400, "station required")
		return
	}
	if atMin < -1440 || atMin > 3*1440 {
		writeError(w, 400, "at out of range")
		return
	}
	_, _, anchor, ok := d.dayWindow(w, r)
	if !ok {
		return
	}
	eva, known := d.resolveStation(r.Context(), station)
	if !known {
		writeError(w, 404, "unknown station")
		return
	}
	at := anchor.Add(time.Duration(atMin) * time.Minute)
	lo := at.Add(-150 * time.Minute)
	hi := at.Add(240 * time.Minute)

	// Departures in the planned-time window; per stop the latest state ≤
	// knowledge time. Direction/route come from a LATERAL join over the
	// best route-bearing snapshot of the same stop (tl-less delta rows
	// often carry an empty route while earlier snapshots know the full one).
	//
	// File window: the board needs snapshots observed from the previous
	// evening (early departures) up to the knowledge time.
	se := d.st.StopEvents(anchor.AddDate(0, 0, -1), at)
	rows, err := d.st.DB.QueryContext(r.Context(), `
		SELECT DISTINCT ON (se.stop_id)
		       se.stop_id, st.name, se.line_category, `+labelExpr+`,
		       COALESCE(rv.direction_name, '') AS direction_name, rv.via_path,
		       se.planned_time, se.actual_time, se.cancelled, se.scraped_at,
		       COALESCE(se.platform, ''), COALESCE(se.planned_platform, '')
		FROM `+se+` se
		JOIN stations st ON st.eva = se.station_eva
		LEFT JOIN LATERAL (
			SELECT se2.direction_name, se2.via_path
			FROM `+se+` se2
			WHERE se2.station_eva = se.station_eva
			  AND se2.stop_id = se.stop_id -- direction ignored: the route (ppth/cpth) often sits only on the arrival row
			  AND se2.scraped_at <= $4
			  AND (NULLIF(se2.direction_name, '') IS NOT NULL OR COALESCE(len(se2.via_path), 0) > 0)
			ORDER BY COALESCE(len(se2.via_path), 0) DESC, se2.scraped_at DESC -- prefer route-bearing rows
			LIMIT 1
		) rv ON true
		WHERE se.station_eva = COALESCE(NULLIF($5, ''), (SELECT eva FROM stations WHERE name = $1 LIMIT 1))
		  AND se.direction = 'departure'
		  AND se.planned_time BETWEEN $2 AND $3
		  AND se.scraped_at <= $4
		ORDER BY se.stop_id, (se.line_category = '') ASC, se.scraped_at DESC`, station, lo, hi, at, eva)
	if err != nil {
		writeQueryError(w, "board query", err)
		return
	}
	defer rows.Close()

	raw := []rawRow{}
	for rows.Next() {
		row, err := scanRaw(rows)
		if err != nil {
			writeQueryError(w, "board scan", err)
			return
		}
		raw = append(raw, row)
	}
	if err := rows.Err(); err != nil {
		writeQueryError(w, "board query", err)
		return
	}

	writeJSONCached(w, r, ageData, buildBoard(raw, anchor))
}

// buildBoard: pure aggregation (tested).
func buildBoard(raw []rawRow, anchor time.Time) uiBoardData {
	sort.Slice(raw, func(i, j int) bool { return raw[i].Planned.Before(raw[j].Planned) })
	out := uiBoardData{Rows: []uiBoardRow{}, Lines: []uiLineInfo{}}
	seenLines := map[string]bool{}
	for _, s := range raw {
		row := uiBoardRow{
			RunID:     tripKey(s.StopID),
			StopIdx:   stopIdxOf(s.StopID),
			LineKey:   lineKeyOf(s.Cat, s.Label),
			LineLabel: lineLabelOf(s.Cat, s.Label),
			Planned:   minutesOfDay(s.Planned, anchor),
			Plat:      s.Platform,
			PlatFrom:  s.PlatFrom,
			Dest: func() string { // via_path ends at the destination — S-Bahn often has no direction_name
				if s.Dest != "" {
					return s.Dest
				}
				if len(s.Via) > 0 {
					return s.Via[len(s.Via)-1]
				}
				return ""
			}(),
			Via:       strings.Join(firstN(s.Via, 2), " – "),
			Cancelled: s.Cancelled,
		}
		row.Known = s.Actual != nil || s.Cancelled
		if s.Actual != nil {
			// ROUND instead of truncate: S-Bahn delays < 60 s would
			// otherwise all collapse to +0 (the P50/P90 bug).
			delay := int(s.Actual.Sub(s.Planned).Round(time.Minute) / time.Minute)
			row.Delay = &delay
			row.Ist = !s.Scraped.Before(*s.Actual)
		}
		row.PlatChanged = row.Plat != "" && row.PlatFrom != "" && row.Plat != row.PlatFrom
		out.Rows = append(out.Rows, row)

		if !seenLines[row.LineKey] && s.Cat != "" { // tl-less rows produce no empty chip
			seenLines[row.LineKey] = true
			out.Lines = append(out.Lines, uiLineInfo{Key: row.LineKey, Label: row.LineLabel, Cat: s.Cat})
		}
	}
	return out
}

// scanRaw reads a row in the standard field layout of the data-UI queries.
func scanRaw(rows interface {
	Scan(dest ...any) error
}) (rawRow, error) {
	var s rawRow
	var via sql.NullString
	var dest string
	err := rows.Scan(&s.StopID, &s.Name, &s.Cat, &s.Label, &dest, &via,
		&s.Planned, &s.Actual, &s.Cancelled, &s.Scraped, &s.Platform, &s.PlatFrom)
	s.Dest = dest
	s.Via = splitVia(via)
	return s, err
}

// ---------------------------------------------------------------------------
// Journey (trip view)
// ---------------------------------------------------------------------------

func (d *dataUI) handleJourney(w http.ResponseWriter, r *http.Request) {
	run := strings.TrimSpace(r.URL.Query().Get("run"))
	if !runIDRe.MatchString(run) {
		writeError(w, 400, "run required (format <tripId>-<YYMMddHHmm>)")
		return
	}
	station := strings.TrimSpace(r.URL.Query().Get("station"))
	if station != "" {
		if _, known := d.resolveStation(r.Context(), station); !known {
			writeError(w, 404, "unknown station")
			return
		}
	}

	// File window: the run's service day ± 1 (late-night snapshots of the
	// trip live in the next day's files, early ones in the previous).
	runDay := runDayAnchor(run)
	se := d.st.StopEvents(runDay.AddDate(0, 0, -1), runDay.AddDate(0, 0, 2))
	rows, err := d.st.DB.QueryContext(r.Context(), `
		SELECT se.stop_id, se.direction, se.station_eva, st.name,
		       se.planned_time, se.actual_time, se.cancelled, se.scraped_at,
		       COALESCE(se.platform, ''), COALESCE(se.planned_platform, ''),
		       se.line_category, `+labelExpr+`,
		       se.via_path, COALESCE(se.direction_name, '')
		FROM `+se+` se
		JOIN stations st ON st.eva = se.station_eva
		WHERE se.stop_id LIKE $1 || '-%'
		ORDER BY se.stop_id, se.scraped_at`, run)
	if err != nil {
		writeQueryError(w, "journey query", err)
		return
	}
	defer rows.Close()

	raw := []rawRow{}
	for rows.Next() {
		row, err := scanJourney(rows)
		if err != nil {
			writeQueryError(w, "journey scan", err)
			return
		}
		raw = append(raw, row)
	}
	if err := rows.Err(); err != nil {
		writeQueryError(w, "journey query", err)
		return
	}

	runOut, line, ok := buildJourney(run, raw)
	if !ok {
		writeError(w, 404, "run not found")
		return
	}

	// altLines for the run's own service day.
	alt, scoped := d.altLinesFor(r.Context(), runDay, line, station)

	writeJSONCached(w, r, ageData, uiJourneyData{Run: runOut, Line: line, AltLines: alt, AltScoped: scoped})
}

// scanJourney: layout of the journey query (direction before name).
func scanJourney(rows interface {
	Scan(dest ...any) error
}) (rawRow, error) {
	var s rawRow
	var via sql.NullString
	err := rows.Scan(&s.StopID, &s.Direction, &s.Eva, &s.Name,
		&s.Planned, &s.Actual, &s.Cancelled, &s.Scraped,
		&s.Platform, &s.PlatFrom, &s.Cat, &s.Label, &via, &s.Dest)
	s.Via = splitVia(via)
	return s, err
}

// buildJourney: pure aggregation of all rows of one run (tested).
func buildJourney(runID string, raw []rawRow) (uiRun, uiLineInfo, bool) {
	if len(raw) == 0 {
		return uiRun{}, uiLineInfo{}, false
	}

	// Group stops (stop_id); departure preferred over arrival.
	deps := map[string][]rawRow{}
	arrs := map[string][]rawRow{}
	for _, s := range raw {
		if s.Direction == "departure" {
			deps[s.StopID] = append(deps[s.StopID], s)
		} else {
			arrs[s.StopID] = append(arrs[s.StopID], s)
		}
	}

	stops := make([]uiStop, 0, len(deps)+len(arrs))
	seen := map[string]bool{}
	for _, s := range raw {
		if seen[s.StopID] {
			continue
		}
		seen[s.StopID] = true
		list := deps[s.StopID]
		if len(list) == 0 {
			list = arrs[s.StopID]
		}
		if len(list) == 0 {
			continue
		}
		stops = append(stops, buildStop(s.StopID, list))
	}
	sort.Slice(stops, func(i, j int) bool { return stops[i].Idx < stops[j].Idx })

	// Anchor = Berlin midnight of the day the trip STARTS (consistent
	// across midnight).
	anchor := dayAnchor(stops[0].plannedTS)
	for i := range stops {
		s := &stops[i]
		s.Planned = minutesOfDay(s.plannedTS, anchor)
		s.Actual = s.Planned + s.FinalD
		for j := range s.Snaps {
			s.Snaps[j].At = minutesOfDay(s.Snaps[j].atTS, anchor)
		}
	}

	// Line metadata from the first row carrying a category.
	var line uiLineInfo
	for _, s := range raw {
		if s.Cat != "" {
			ctx := strings.Join(firstN(s.Via, 2), " – ")
			if s.Dest != "" {
				ctx = strings.TrimSpace(strings.TrimSuffix(ctx, " – "+firstOf(s.Via, s.Dest)))
			}
			line = uiLineInfo{
				Key:   lineKeyOf(s.Cat, s.Label),
				Label: lineLabelOf(s.Cat, s.Label),
				Cat:   s.Cat,
				Ctx:   ctx,
			}
			break
		}
	}

	out := uiRun{
		ID:        runID,
		LineKey:   line.Key,
		Dep:       stops[0].Planned,
		Cancelled: allCancelled(stops),
		Stops:     stops,
	}
	return out, line, true
}

func firstOf(v []string, fallback string) string {
	if len(v) > 0 {
		return v[0]
	}
	return fallback
}

func allCancelled(stops []uiStop) bool {
	for _, s := range stops {
		if !s.Cancelled {
			return false
		}
	}
	return len(stops) > 0
}

// buildStop: pure aggregation of one snapshot series (tested).
func buildStop(stopID string, list []rawRow) uiStop {
	sort.Slice(list, func(i, j int) bool { return list[i].Scraped.Before(list[j].Scraped) })
	st := uiStop{
		Idx:       stopIdxOf(stopID),
		Name:      list[0].Name,
		Eva:       list[0].Eva,
		PlatFrom:  list[0].PlatFrom,
		PlatKnown: 1 << 30,
		Snaps:     make([]uiSnap, 0, len(list)),
	}
	if st.PlatFrom == "" {
		st.PlatFrom = list[0].Platform
	}
	lastPlat := st.PlatFrom
	for i, s := range list {
		snap := uiSnap{
			Ist:       s.Actual != nil && !s.Scraped.Before(*s.Actual),
			Cancelled: s.Cancelled,
			Plat:      s.Platform,
			atTS:      s.Scraped,
		}
		if snap.Plat == "" {
			snap.Plat = s.PlatFrom
		}
		if s.Actual != nil {
			d := int(s.Actual.Sub(s.Planned).Round(time.Minute) / time.Minute)
			snap.Prog = &d
			st.FinalD = d
			st.Cancelled = false
		}
		if snap.Plat != lastPlat {
			if st.PlatKnown == 1<<30 {
				st.PlatKnown = i
			}
			lastPlat = snap.Plat
		}
		st.Snaps = append(st.Snaps, snap)
	}
	// Cancellation = state of the LAST snapshot (re-instatements happen),
	// but an actual time always wins.
	st.Cancelled = st.Snaps[len(st.Snaps)-1].Cancelled
	st.PlatTo = lastPlat
	if st.PlatTo == "" {
		st.PlatTo = st.PlatFrom
	}
	st.plannedTS = list[0].Planned
	return st
}

// ---------------------------------------------------------------------------
// Line view
// ---------------------------------------------------------------------------

func (d *dataUI) handleLine(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("line"))
	cat, label := splitLineKey(key)
	if cat == "" || len(key) > 64 {
		writeError(w, 400, "line required (CAT|LABEL)")
		return
	}
	station := strings.TrimSpace(r.URL.Query().Get("station"))
	start, end, anchor, ok := d.dayWindow(w, r)
	if !ok {
		return
	}
	if station != "" {
		if _, known := d.resolveStation(r.Context(), station); !known {
			writeError(w, 404, "unknown station")
			return
		}
	}

	all, err := d.lineRows(r.Context(), start, end, cat, label)
	if err != nil {
		writeQueryError(w, "line query", err)
		return
	}

	// Station scope: only runs that stop there (after the cache — all
	// scopes share one scan).
	raw := all
	if station != "" {
		raw = filterByStation(all, station)
	}

	out := buildLine(raw, anchor, key, cat, label)
	if len(out.Runs) == 0 {
		writeError(w, 404, "line not found on this day")
		return
	}

	out.AltLines, out.AltScoped = d.altLinesFor(r.Context(), anchor, out.Line, station)
	writeJSONCached(w, r, ageData, out)
}

// lineRows: latest snapshot per stop of every run of one line on one day —
// cached, singleflight, behind the heavy semaphore.
//
// The label filter runs in SQL (same expression as labelExpr): the transfer
// to Go shrinks from the full day to a few thousand rows. via_path/
// direction_name/platform are not needed by buildLine → not selected.
func (d *dataUI) lineRows(ctx context.Context, start, end time.Time, cat, label string) ([]rawRow, error) {
	key := start.Format("2006-01-02") + "|" + cat + "|" + label
	if v, ok := d.lineCache.get(key, time.Now()); ok {
		return v, nil
	}
	return d.lineSF.Do(ctx, key, func() ([]rawRow, error) {
		ctx, cancel := context.WithTimeout(context.Background(), heavyWait+heavyLineTimeout+5*time.Second)
		defer cancel()
		release, err := d.acquireHeavy(ctx, heavyWait)
		if err != nil {
			return nil, err
		}
		defer release()
		raw := []rawRow{}
		// File window: snapshots observed from the previous evening
		// (early runs) through the end of the day (post-midnight tail).
		se := d.st.StopEvents(start.AddDate(0, 0, -1), end)
		rows, err := d.st.DB.QueryContext(ctx, `
			SELECT DISTINCT ON (se.stop_id)
			       se.stop_id, se.direction, se.station_eva, st.name,
			       se.planned_time, se.actual_time, se.cancelled, se.scraped_at,
			       '' AS platform, '' AS planned_platform,
			       se.line_category, `+labelExprSE+`,
			       NULL::VARCHAR, ''
			FROM `+se+` se
			JOIN stations st ON st.eva = se.station_eva
			WHERE se.scraped_at >= $1 AND se.scraped_at < $2
			  AND se.line_category = $3
			  AND `+labelExprSE+` = $5
			  AND right(regexp_replace(se.stop_id, '-[0-9]+$', ''), 10) LIKE $4 -- service day
			ORDER BY se.stop_id, se.scraped_at DESC`,
			start, end, cat, start.In(berlinLoc).Format("060102")+"%", label)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanJourney(rows)
			if err != nil {
				return nil, err
			}
			raw = append(raw, row)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		d.lineCache.put(key, raw, time.Now())
		return raw, nil
	})
}

// filterByStation keeps only rows of runs that stop at the station.
func filterByStation(raw []rawRow, station string) []rawRow {
	trips := map[string]bool{}
	for _, s := range raw {
		if s.Name == station {
			trips[tripKey(s.StopID)] = true
		}
	}
	out := []rawRow{}
	for _, s := range raw {
		if trips[tripKey(s.StopID)] {
			out = append(out, s)
		}
	}
	return out
}

// buildLine: pure aggregation (tested).
func buildLine(raw []rawRow, anchor time.Time, key, cat, label string) uiLineData {
	// per run: sort stops, end delay from the last stop
	type trip struct {
		id      string
		stops   []uiLineRunStop
		idxs    []int
		cxLast  bool
		firstPt time.Time
	}
	byTrip := map[string]*trip{}
	var order []string
	for _, s := range raw {
		tk := tripKey(s.StopID)
		t := byTrip[tk]
		if t == nil {
			t = &trip{id: tk}
			byTrip[tk] = t
			order = append(order, tk)
		}
		finalD := 0
		if s.Actual != nil {
			finalD = int(s.Actual.Sub(s.Planned).Round(time.Minute) / time.Minute)
		}
		t.stops = append(t.stops, uiLineRunStop{Name: s.Name, FinalD: finalD, Cancelled: s.Cancelled})
		t.idxs = append(t.idxs, stopIdxOf(s.StopID))
		if t.firstPt.IsZero() {
			t.firstPt = s.Planned
		}
	}

	runs := []uiLineRun{}
	for _, tk := range order {
		t := byTrip[tk]
		// sort by the stop-index component of stop_id
		idx := make([]int, len(t.stops))
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, b int) bool { return t.idxs[idx[a]] < t.idxs[idx[b]] })
		sorted := make([]uiLineRunStop, len(idx))
		for i, ix := range idx {
			sorted[i] = t.stops[ix]
		}
		// End delay: last stop with an actual time.
		finalD := 0
		for i := len(sorted) - 1; i >= 0; i-- {
			if sorted[i].FinalD > 0 {
				finalD = sorted[i].FinalD
				break
			}
		}
		dep := 0
		if len(sorted) > 0 {
			dep = minutesOfDay(t.firstPt, anchor)
		}
		cancelled := len(sorted) > 0 && sorted[len(sorted)-1].Cancelled
		runs = append(runs, uiLineRun{ID: tk, Dep: dep, Cancelled: cancelled, FinalD: finalD, Stops: sorted})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Dep < runs[j].Dep })

	fin := []int{}
	cx := 0
	for _, run := range runs {
		if run.Cancelled {
			cx++
		} else {
			fin = append(fin, run.FinalD)
		}
	}

	out := uiLineData{
		Line: uiLineInfo{Key: key, Label: lineLabelOf(cat, label), Cat: cat},
		Runs: runs,
		Stats: uiLineStats{
			N:    len(runs),
			Cx:   cx,
			P50:  percentile(fin, 50),
			P90:  percentile(fin, 90),
			Max:  maxInt(fin),
			Punc: puncOf(fin),
		},
		AltLines: []uiAltLine{},
	}

	// Context (disambiguation) from the first run.
	if len(runs) > 0 && len(runs[0].Stops) > 1 {
		s := runs[0].Stops
		mid := s[len(s)/2]
		last := s[len(s)-1]
		out.Ctx = mid.Name + " ⇢ " + last.Name
		out.Line.Ctx = out.Ctx
	}
	return out
}

// ---------------------------------------------------------------------------
// Day overview
// ---------------------------------------------------------------------------

func (d *dataUI) handleDay(w http.ResponseWriter, r *http.Request) {
	start, end, _, ok := d.dayWindow(w, r)
	if !ok {
		return
	}
	day, err := d.loadDay(r.Context(), start, end)
	if err != nil {
		writeQueryError(w, "day aggregation", err)
		return
	}
	writeJSONCached(w, r, ageData, day)
}

// loadDay returns the day aggregate from the cache or computes it —
// singleflight, detached context with its own budget, behind the heavy
// semaphore.
func (d *dataUI) loadDay(ctx context.Context, start, end time.Time) (dayData, error) {
	key := start.Format("2006-01-02")
	if v, ok := d.dayCache.get(key, time.Now()); ok {
		return v, nil
	}
	return d.daySF.Do(ctx, key, func() (dayData, error) {
		ctx, cancel := context.WithTimeout(context.Background(), heavyWait+heavyDayTimeout+5*time.Second)
		defer cancel()
		release, err := d.acquireHeavy(ctx, heavyWait)
		if err != nil {
			return dayData{}, err
		}
		defer release()
		day, err := computeDay(ctx, d.st, start, end)
		if err != nil {
			return dayData{}, fmt.Errorf("computeDay %s: %w", key, err)
		}
		day.TripsOfDay = nil                 // intermediate result only — not held in the cache
		d.dayCache.put(key, day, time.Now()) // AFTER the computation
		return day, nil
	})
}

// computeDay: latest snapshot per stop → per run start and end.
func computeDay(ctx context.Context, st *store.Store, start, end time.Time) (dayData, error) {
	// SERVICE-day filter: the date part of the trip id must match the day —
	// otherwise yesterday's post-midnight remnants land in today's aggregate.
	dayPat := start.In(berlinLoc).Format("060102") + "%"
	// File window: previous evening (early runs) through the day itself
	// (dayOf(end) includes the next day's files for post-midnight snapshots).
	se := st.StopEvents(start.AddDate(0, 0, -1), end)

	rows, err := st.DB.QueryContext(ctx, `
		WITH day_last AS (
			SELECT DISTINCT ON (se.stop_id)
			       se.stop_id, se.direction,
			       se.planned_time, se.actual_time, se.cancelled,
			       se.line_category,
			       `+labelExpr+` AS line_label,
			       st.name AS station_name
			FROM `+se+` se
			JOIN stations st ON st.eva = se.station_eva
			WHERE se.scraped_at >= $1 AND se.scraped_at < $2
			  AND se.line_category <> '' -- exclude fchg delta rows without tl
			  AND right(regexp_replace(se.stop_id, '-[0-9]+$', ''), 10) LIKE $3 -- service day
			ORDER BY se.stop_id, se.scraped_at DESC
		), numbered AS (
			SELECT *,
			       regexp_replace(stop_id, '-[0-9]+$', '') AS trip,
			       CAST(regexp_replace(stop_id, '^.*-', '') AS BIGINT) AS stop_idx
			FROM day_last
		), first AS (
			SELECT DISTINCT ON (trip) trip, cat, label, dep_min, from_name, cancelled
			FROM (
				SELECT trip, line_category AS cat, line_label AS label,
				       CAST(EXTRACT(EPOCH FROM (planned_time - $1)) / 60 AS BIGINT) AS dep_min,
				       station_name AS from_name, cancelled, stop_idx
				FROM numbered WHERE direction = 'departure'
			) f
			ORDER BY trip, stop_idx
		), last AS (
			SELECT DISTINCT ON (trip) trip, to_name, delay, cancelled
			FROM (
				SELECT trip, station_name AS to_name,
				       CASE WHEN actual_time IS NULL THEN NULL
				            ELSE CAST(ROUND(EXTRACT(EPOCH FROM (actual_time - planned_time)) / 60) AS BIGINT) END AS delay,
				       cancelled, stop_idx
				FROM numbered
			) l
			ORDER BY trip, stop_idx DESC
		)
		SELECT f.trip, f.cat, f.label, f.dep_min, f.from_name,
		       l.to_name, COALESCE(l.delay, 0), (f.cancelled AND l.cancelled)
		FROM first f JOIN last l USING (trip)`, start, end, dayPat)
	if err != nil {
		return dayData{}, err
	}
	defer rows.Close()

	day := dayData{
		TripsOfDay:  []tripFact{},
		Hours:       []uiHourBar{},
		Leaderboard: []uiLeaderRow{},
		Top:         []uiTopRow{},
	}
	hours := map[int][]int{}
	perLine := map[string][]int{}
	lineMeta := map[string]*uiLeaderRow{}

	for rows.Next() {
		var tf tripFact
		var depMin int
		var bothCx bool
		if err := rows.Scan(&tf.Trip, &tf.Cat, &tf.Label, &depMin, &tf.DepFrom, &tf.EndTo, &tf.FinalD, &bothCx); err != nil {
			return dayData{}, err
		}
		tf.Cancelled = bothCx
		tf.DepMin = depMin
		day.TripsOfDay = append(day.TripsOfDay, tf)
		if tf.Cancelled {
			continue
		}
		h := ((depMin % 1440) + 1440) % 1440 / 60
		hours[h] = append(hours[h], tf.FinalD)

		key := lineKeyOf(tf.Cat, tf.Label)
		perLine[key] = append(perLine[key], tf.FinalD)
		if lineMeta[key] == nil {
			lineMeta[key] = &uiLeaderRow{Key: key, Label: lineLabelOf(tf.Cat, tf.Label)}
		}
	}
	if err := rows.Err(); err != nil {
		return dayData{}, err
	}
	for _, tf := range day.TripsOfDay {
		key := lineKeyOf(tf.Cat, tf.Label)
		if lineMeta[key] == nil {
			lineMeta[key] = &uiLeaderRow{Key: key, Label: lineLabelOf(tf.Cat, tf.Label)}
		}
		lineMeta[key].N++
		if tf.Cancelled {
			lineMeta[key].Cx++
		}
	}

	all := []int{}
	for _, v := range hours {
		all = append(all, v...)
	}
	day.Kpis = uiDayKpis{
		Runs:  len(day.TripsOfDay),
		Punc:  puncOf(all),
		Avg:   round1(avgOf(all)),
		Cx:    countCx(day.TripsOfDay),
		Snaps: countSnaps(ctx, st, start, end),
	}

	hs := make([]int, 0, len(hours))
	for h := range hours {
		hs = append(hs, h)
	}
	sort.Ints(hs)
	for _, h := range hs {
		day.Hours = append(day.Hours, uiHourBar{H: h, Avg: round1(avgOf(hours[h])), N: len(hours[h])})
	}

	for key, fin := range perLine {
		e := lineMeta[key]
		if e.N < 3 { // 1-2-trip noise (operational-number fallback) skews the ranking
			continue
		}
		e.P50, e.P90, e.Max = percentile(fin, 50), percentile(fin, 90), maxInt(fin)
		e.Punc = puncOf(fin)
		day.Leaderboard = append(day.Leaderboard, *e)
	}
	sort.Slice(day.Leaderboard, func(i, j int) bool { return day.Leaderboard[i].P90 > day.Leaderboard[j].P90 })

	top := make([]tripFact, 0)
	for _, tf := range day.TripsOfDay {
		if !tf.Cancelled {
			top = append(top, tf)
		}
	}
	sort.Slice(top, func(i, j int) bool { return top[i].FinalD > top[j].FinalD })
	for i, tf := range top {
		if i >= 8 {
			break
		}
		day.Top = append(day.Top, uiTopRow{
			RunID: tf.Trip, Label: lineLabelOf(tf.Cat, tf.Label),
			Dep: tf.DepMin, From: tf.DepFrom, To: tf.EndTo, FinalD: tf.FinalD,
		})
	}

	return day, nil
}

func countSnaps(ctx context.Context, st *store.Store, start, end time.Time) int {
	var n int
	if err := st.DB.QueryRowContext(ctx, `
		SELECT count(*) FROM `+st.StopEvents(start, end)+`
		WHERE scraped_at >= $1 AND scraped_at < $2`,
		start, end).Scan(&n); err != nil {
		return 0
	}
	return n
}

// ---------------------------------------------------------------------------
// Quick switch: lines at a station (cached per day+station)
// ---------------------------------------------------------------------------

// altLinesFor: lines departing at the station on the given day — per line
// the run departing closest to the day's start. Order: by number of
// departures at the station (most frequent first), so cutting the list in
// the frontend never shows only one category.
func (d *dataUI) altLinesFor(ctx context.Context, anchor time.Time, line uiLineInfo, station string) ([]uiAltLine, bool) {
	if station == "" {
		return []uiAltLine{}, false
	}
	eva, known := d.resolveStation(ctx, station)
	if !known {
		return []uiAltLine{}, false
	}
	start, end := anchor, anchor.AddDate(0, 0, 1)
	key := start.Format("2006-01-02") + "|" + eva + "|" + station

	lines, ok := d.stationLn.get(key, time.Now())
	if !ok {
		release, err := d.acquireHeavy(ctx, heavyWait)
		if err != nil {
			return []uiAltLine{}, false // the quick switch is optional — don't fail the response
		}
		lines, err = d.queryAltLines(ctx, eva, station, start, end)
		release()
		if err != nil {
			log.Printf("[api] altLines: %v", err)
			return []uiAltLine{}, false
		}
		d.stationLn.put(key, lines, time.Now())
	}

	scoped := false
	for _, l := range lines {
		if l.Key == line.Key {
			scoped = true
			break
		}
	}
	return lines, scoped
}

func (d *dataUI) queryAltLines(ctx context.Context, eva, station string, start, end time.Time) ([]uiAltLine, error) {
	se := d.st.StopEvents(start.AddDate(0, 0, -1), end)
	rows, err := d.st.DB.QueryContext(ctx, `
		WITH ev AS (
			SELECT se.line_category AS cat, `+labelExprSE+` AS label, se.stop_id, se.planned_time
			FROM `+se+` se
			WHERE se.station_eva = COALESCE(NULLIF($6, ''), (SELECT eva FROM stations WHERE name = $1 LIMIT 1))
			  AND se.direction = 'departure'
			  AND se.planned_time >= $3::TIMESTAMPTZ - INTERVAL '3 hours'
			  AND se.planned_time <  $4::TIMESTAMPTZ + INTERVAL '12 hours'
			  AND se.scraped_at >= $3 AND se.scraped_at < $4
			  AND right(regexp_replace(se.stop_id, '-[0-9]+$', ''), 10) LIKE $5 -- service day
			  AND se.line_category <> '' -- exclude fchg delta rows without tl
		), cnt AS (
			SELECT cat, label, count(DISTINCT stop_id) AS n FROM ev GROUP BY cat, label
		), pick AS (
			SELECT DISTINCT ON (cat, label)
			       cat, label, regexp_replace(stop_id, '-[0-9]+$', '') AS trip,
			       CAST(EXTRACT(EPOCH FROM (planned_time - $2)) / 60 AS BIGINT) AS dep_min
			FROM ev
			ORDER BY cat, label, ABS(CAST(EXTRACT(EPOCH FROM (planned_time - $2)) AS BIGINT))
		)
		SELECT p.cat, p.label, p.trip, p.dep_min
		FROM pick p JOIN cnt c USING (cat, label)
		ORDER BY c.n DESC, p.cat, p.label`,
		station, start, start, end, start.In(berlinLoc).Format("060102")+"%", eva)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []uiAltLine{}
	for rows.Next() {
		var cat, label, trip string
		var depMin int
		if err := rows.Scan(&cat, &label, &trip, &depMin); err != nil {
			return nil, err
		}
		lines = append(lines, uiAltLine{
			Key: lineKeyOf(cat, label), Label: lineLabelOf(cat, label),
			Cat: cat, RunID: trip, Dep: depMin,
		})
	}
	return lines, rows.Err()
}

// runDayAnchor: the service day from the run id (…-YYMMddHHmm) as Berlin
// midnight. Fallback: today (which has no data — the query returns empty).
func runDayAnchor(run string) time.Time {
	if len(run) >= 10 {
		if t, err := time.ParseInLocation("0601021504", run[len(run)-10:], berlinLoc); err == nil {
			return dayAnchor(t)
		}
	}
	return dayAnchor(time.Now())
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func firstN(v []string, n int) []string {
	if len(v) <= n {
		return v
	}
	return v[:n]
}

func splitLineKey(key string) (cat, label string) {
	i := strings.Index(key, "|")
	if i < 0 {
		return key, ""
	}
	return key[:i], key[i+1:]
}

func percentile(arr []int, p int) int {
	if len(arr) == 0 {
		return 0
	}
	s := append([]int{}, arr...)
	sort.Ints(s)
	return s[min(len(s)-1, (p*len(s))/100)]
}

func maxInt(arr []int) int {
	m := 0
	for _, v := range arr {
		if v > m {
			m = v
		}
	}
	return m
}

func puncOf(arr []int) int {
	if len(arr) == 0 {
		return 0
	}
	n := 0
	for _, v := range arr {
		if v <= 5 {
			n++
		}
	}
	return 100 * n / len(arr)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func avgOf(arr []int) float64 {
	if len(arr) == 0 {
		return 0
	}
	s := 0
	for _, v := range arr {
		s += v
	}
	return float64(s) / float64(len(arr))
}

func countCx(t []tripFact) int {
	n := 0
	for _, tf := range t {
		if tf.Cancelled {
			n++
		}
	}
	return n
}

// JSON encoding of dayData without internal fields:
func (d dayData) MarshalJSON() ([]byte, error) {
	type alias struct {
		Kpis        uiDayKpis     `json:"kpis"`
		Hours       []uiHourBar   `json:"hours"`
		Leaderboard []uiLeaderRow `json:"leaderboard"`
		Top         []uiTopRow    `json:"top"`
	}
	return json.Marshal(alias{Kpis: d.Kpis, Hours: d.Hours, Leaderboard: d.Leaderboard, Top: d.Top})
}
