package api

// Search (/api/search) — a pure RAM index inside the API process, NO SQL
// per keystroke.
//
// Data sources:
//   - Station index: the stations table (~5.4k rows), loaded lazily on
//     first use with retry after failures (stationStore). The dataset is
//     immutable — no periodic refresh.
//   - Day index (lines, runs, train numbers, stops per run) per calendar
//     day, built lazily on the first search request for that day. Data is
//     immutable: each day is read exactly once (one query per day window)
//     and then cached in RAM (bounded, LRU). Until the first build
//     finishes, searches answer pending=true — the frontend re-asks.
//
// Index query and cost (dayIndexSQL): a plain scan of the day window's
// rows with a WHERE on scraped_at. Deduplication (per stop_id) and the
// service-day filter happen in Go while accumulating.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// JSON contracts
// ---------------------------------------------------------------------------

type uiSearchResults struct {
	Stations []uiSearchStation `json:"stations"`
	Lines    []uiSearchLine    `json:"lines"`

	// When the query contained a time ("porz 14:30"):
	Time *int `json:"time,omitempty"` // minutes of the day

	// When a station matched: the lines stopping there (most frequent first).
	StationName  string         `json:"stationName,omitempty"`
	StationLines []uiSearchLine `json:"stationLines,omitempty"`

	// Combined "line at station" hits ("horrem s12") — only lines that
	// actually stop there according to the day index.
	LineAtStation []uiLineAtStation `json:"lineAtStation,omitempty"`

	// Individual runs: train-number search ("ICE 579", "579", "10123") or
	// line+station+time ("horrem s12 14:30" → next matching departures).
	Trips []uiSearchTrip `json:"trips,omitempty"`

	// The day index is being built for the first time: lines/runs may still
	// be missing — the frontend may re-ask after a few seconds.
	Pending bool `json:"pending,omitempty"`
}

type uiSearchStation struct {
	Name string `json:"name"`
	N    int    `json:"n"` // runs stopping here that day (0 = unknown)
}

type uiSearchLine struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cat   string `json:"cat"`
	Ctx   string `json:"ctx"`
	N     int    `json:"n"`
}

type uiSearchTrip struct {
	RunID   string `json:"runId"`
	Day     string `json:"day"`
	Key     string `json:"key"`
	Label   string `json:"label"`
	Cat     string `json:"cat"`
	Number  string `json:"number"`
	From    string `json:"from"`
	To      string `json:"to"`
	Dep     string `json:"dep"`
	Station string `json:"station,omitempty"`
	StopIdx int    `json:"stopIdx,omitempty"`
}

const (
	maxStations      = 8
	maxStationsBoard = 10 // kind=stations (board combobox)
	maxLines         = 6
	maxTrips         = 8
	maxLineAtStation = 3
	maxStationLines  = 6
)

// ---------------------------------------------------------------------------
// Normalization: fold, split into words, synonyms
// ---------------------------------------------------------------------------

var foldRepl = strings.NewReplacer(
	"ä", "a", "ö", "o", "ü", "u", "ß", "ss", "Ä", "a", "Ö", "o", "Ü", "u",
	"é", "e", "è", "e", "ê", "e", "ë", "e", "à", "a", "â", "a", "á", "a",
	"ç", "c", "ô", "o", "ó", "o", "ï", "i", "î", "i", "í", "i", "ñ", "n", "ú", "u", "û", "u")

var aeRepl = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "Ä", "ae", "Ö", "oe", "Ü", "ue", "ß", "ss")

// foldWords: lowercase + diacritics-free, split at every non-[a-z0-9] AND at
// letter↔digit boundaries ("s12" → s,12; "ice579" → ice,579;
// "Frankfurt(Main)Hbf" → frankfurt,main,hbf). ae=true folds ä→ae etc.
func foldWords(s string, ae bool) []string {
	if ae {
		s = aeRepl.Replace(s)
	}
	s = strings.ToLower(foldRepl.Replace(s))
	var out []string
	var cur []byte
	kind := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		k := 0
		if c >= 'a' && c <= 'z' {
			k = 1
		} else if c >= '0' && c <= '9' {
			k = 2
		}
		if (k == 0 || k != kind) && len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
		if k != 0 {
			cur = append(cur, c)
		}
		kind = k
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// synonyms: applied on both sides (station name and query).
var synonyms = map[string][]string{
	"hauptbahnhof": {"hbf"},
	"bahnhof":      {"bf"},
	"sankt":        {"st"},
	"ffm":          {"frankfurt", "main"},
	"westfalen":    {"westf"},
	"rheinland":    {"rhld"},
	"sachsen":      {"sachs"},
	"airport":      {"flughafen"},
	"flugh":        {"flughafen"},
}

func canonWords(ws []string) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		if s, ok := synonyms[w]; ok {
			out = append(out, s...)
		} else {
			out = append(out, w)
		}
	}
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isLetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Station index
// ---------------------------------------------------------------------------

type stationEntry struct {
	Eva    string
	Name   string
	Cat    int // StaDa category 1 (large) … 7; 8 = unknown
	NoIris bool

	forms  [2][]string // canonical words: a-form ("koln"), ae-form ("koeln")
	joined [2]string
	runes  int
}

type stationIndex struct {
	list   []stationEntry
	byName map[string]int
	byEva  map[string]int
	vocab  []string // all distinct words (for the typo fallback)
	loaded time.Time
}

func newStationIndex(entries []stationEntry, loaded time.Time) *stationIndex {
	idx := &stationIndex{
		list:   entries,
		byName: make(map[string]int, len(entries)),
		byEva:  make(map[string]int, len(entries)),
		loaded: loaded,
	}
	seen := map[string]bool{}
	for i := range idx.list {
		e := &idx.list[i]
		if e.Cat <= 0 || e.Cat > 8 {
			e.Cat = 8
		}
		e.forms[0] = canonWords(foldWords(e.Name, false))
		e.forms[1] = canonWords(foldWords(e.Name, true))
		for f := 0; f < 2; f++ {
			e.joined[f] = strings.Join(e.forms[f], "")
			for _, w := range e.forms[f] {
				if !seen[w] {
					seen[w] = true
					idx.vocab = append(idx.vocab, w)
				}
			}
		}
		e.runes = len([]rune(e.Name))
		idx.byName[e.Name] = i
		idx.byEva[e.Eva] = i
	}
	return idx
}

func loadStationIndex(ctx context.Context, db *sql.DB) (*stationIndex, error) {
	rows, err := db.QueryContext(ctx, `SELECT eva, name, COALESCE(category, 0), no_iris FROM stations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []stationEntry
	for rows.Next() {
		var e stationEntry
		if err := rows.Scan(&e.Eva, &e.Name, &e.Cat, &e.NoIris); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return newStationIndex(list, time.Now()), nil
}

func (x *stationIndex) nameOf(eva string) string {
	if x != nil {
		if i, ok := x.byEva[eva]; ok {
			return x.list[i].Name
		}
	}
	return eva
}

// Match tiers (lower = better):
//
//	T0  query (joined) == name (joined): "kolnhbf"
//	T1  every query word is a prefix of a DIFFERENT station word (AND), and
//	    the first query word hits the first station word
//	T2  like T1 in any order ("hbf koln")
//	T3  query (joined, ≥3 chars) is a substring of the name
//	T4  typos: every query word is within the prefix-bounded
//	    Damerau-Levenshtein distance of a station word (1 from 4, 2 from 8
//	    chars). Only as a fallback when T0–T3 yield NOTHING (otherwise
//	    "munchen hbf" would also pull Mönchengladbach Hbf).
const (
	tierExact = iota
	tierPrefixOrdered
	tierPrefixAny
	tierSubstring
	tierFuzzy
)

type stationHit struct {
	i    int
	tier int
}

func stationTier(e *stationEntry, q []string, qj string) int {
	best := -1
	for f := 0; f < 2; f++ {
		ws := e.forms[f]
		if len(ws) == 0 {
			continue
		}
		if e.joined[f] == qj {
			return tierExact
		}
		if prefixAll(ws, q) {
			t := tierPrefixAny
			if strings.HasPrefix(ws[0], q[0]) {
				t = tierPrefixOrdered
			}
			if best < 0 || t < best {
				best = t
			}
		}
		if best < 0 && len(qj) >= 3 && strings.Contains(e.joined[f], qj) {
			best = tierSubstring
		}
	}
	return best
}

// prefixAll: every query word is a prefix of a distinct station word.
// Longer query words are matched first (greedy, but robust for "st" vs "stuttgart").
func prefixAll(ws, q []string) bool {
	if len(q) > len(ws) {
		return false
	}
	order := make([]int, len(q))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(q[order[a]]) > len(q[order[b]]) })
	var used [16]bool
	if len(ws) > len(used) {
		return false
	}
	for _, qi := range order {
		hit := false
		for i, w := range ws {
			if !used[i] && strings.HasPrefix(w, q[qi]) {
				used[i], hit = true, true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func maxEdits(n int) int {
	switch {
	case n >= 8:
		return 2
	case n >= 4:
		return 1
	}
	return 0
}

// prefixDist: minimal Damerau-Levenshtein distance of q to a prefix of w.
func prefixDist(q, w string, limit int) int {
	a, b := q, w
	if len(b) > len(a)+limit {
		b = b[:len(a)+limit]
	}
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
			rowMin = min(rowMin, cur[j])
		}
		if rowMin > limit {
			return limit + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	best := prev[0]
	for _, v := range prev {
		best = min(best, v)
	}
	return best
}

// match returns all hits, sorted: tier → category (1 = large) → no_iris last
// → runs that day (weight, optional) → name length → name.
func (x *stationIndex) match(words []string, weight map[string]int) []stationHit {
	if x == nil || len(words) == 0 {
		return nil
	}
	q := canonWords(words)
	qj := strings.Join(q, "")
	var hits []stationHit
	for i := range x.list {
		if t := stationTier(&x.list[i], q, qj); t >= 0 {
			hits = append(hits, stationHit{i, t})
		}
	}
	if len(hits) == 0 {
		hits = x.fuzzy(q, nil)
	}
	sort.Slice(hits, func(a, b int) bool {
		ha, hb := hits[a], hits[b]
		if ha.tier != hb.tier {
			return ha.tier < hb.tier
		}
		ea, eb := &x.list[ha.i], &x.list[hb.i]
		if ea.Cat != eb.Cat {
			return ea.Cat < eb.Cat
		}
		if ea.NoIris != eb.NoIris {
			return !ea.NoIris
		}
		if wa, wb := weight[ea.Eva], weight[eb.Eva]; wa != wb {
			return wa > wb
		}
		if ea.runes != eb.runes {
			return ea.runes < eb.runes
		}
		return ea.Name < eb.Name
	})
	return hits
}

// fuzzy: typo fallback over the word vocabulary (~7k words), not per
// station — then mapped back through the stations' words.
func (x *stationIndex) fuzzy(q []string, have []stationHit) []stationHit {
	sets := make([]map[string]bool, len(q))
	for qi, qw := range q {
		m := maxEdits(len(qw))
		set := map[string]bool{}
		for _, w := range x.vocab {
			if strings.HasPrefix(w, qw) || (m > 0 && prefixDist(qw, w, m) <= m) {
				set[w] = true
			}
		}
		if len(set) == 0 {
			return nil
		}
		sets[qi] = set
	}
	got := map[int]bool{}
	for _, h := range have {
		got[h.i] = true
	}
	var out []stationHit
	for i := range x.list {
		if got[i] {
			continue
		}
		e := &x.list[i]
		for f := 0; f < 2; f++ {
			all := true
			for qi := range q {
				hit := false
				for _, w := range e.forms[f] {
					if sets[qi][w] {
						hit = true
						break
					}
				}
				if !hit {
					all = false
					break
				}
			}
			if all {
				out = append(out, stationHit{i, tierFuzzy})
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Day index: lines, runs, train numbers, stops
// ---------------------------------------------------------------------------

type idxStop struct {
	Eva string
	Idx int // IRIS stop index (1-based)
	Min int // planned minute since the day anchor (departure, else arrival)
	Dep bool
}

type idxTrip struct {
	Run     string
	Line    *idxLine
	TrainNo string
	Stops   []idxStop // sorted by Idx
}

func (t *idxTrip) stopAt(eva string) (idxStop, bool) {
	for _, s := range t.Stops {
		if s.Eva == eva {
			return s, true
		}
	}
	return idxStop{}, false
}

type idxLine struct {
	Key, Cat, Label string

	catN   string // normalized category ("re")
	labelN string // normalized label ("rs12", "7")
	fullN  string // catN+labelN ("swers12")
	effCat string // category per label prefix ("rs" for RS12), else catN
	num    string // number part of the label ("12" for RS12, "7")

	Trips    []*idxTrip     // sorted by departure
	Stations map[string]int // eva → stop count
	FromEva  string         // endpoints of the first run (context)
	ToEva    string
}

type dayIndex struct {
	Day        string
	Start, End time.Time
	Built      time.Time

	Lines        []*idxLine
	byKey        map[string]*idxLine
	Trips        map[string]*idxTrip
	byTrainNo    map[string][]*idxTrip
	stationLines map[string][]*idxLine // eva → lines (most frequent first)
	stationTrips map[string]int        // eva → runs stopping there
	cats         map[string]bool       // known category words (normalized)
}

func newLine(cat, label string) *idxLine {
	l := &idxLine{Key: lineKeyOf(cat, label), Cat: cat, Label: label, Stations: map[string]int{}}
	l.catN = strings.Join(foldWords(cat, false), "")
	lw := foldWords(label, false)
	l.labelN = strings.Join(lw, "")
	l.fullN = l.catN + l.labelN
	l.effCat = l.catN
	switch {
	case len(lw) >= 2 && isLetters(lw[0]) && isDigits(lw[1]):
		l.effCat = lw[0]
		l.num = strings.Join(lw[1:], "")
	case len(lw) >= 1 && isDigits(lw[0]):
		l.num = strings.Join(lw, "")
	}
	return l
}

// Accumulator: only ever touched by the (single) build goroutine of a day;
// readers only see immutable dayIndex snapshots.
type accStop struct {
	eva string
	min int
	dep bool
}

type accTrip struct {
	cat, label, trainNo string
	hasTL               bool
	stops               map[int]accStop
}

type dayAcc struct {
	anchor time.Time
	dayPat string // YYMMdd of the service day
	trips  map[string]*accTrip
}

func newDayAcc(anchor time.Time) *dayAcc {
	return &dayAcc{anchor: anchor, dayPat: anchor.In(berlinLoc).Format("060102"), trips: map[string]*accTrip{}}
}

// idxRow: one row of the index query.
type idxRow struct {
	StopID, Eva, Direction  string
	Cat, LineLabel, TrainNo string
	HasTL                   bool
	Planned                 time.Time
}

// splitStopID: "{tripId}-{YYMMddHHmm}-{idx}" → (run, idx).
func splitStopID(stopID string) (string, int, bool) {
	i := strings.LastIndexByte(stopID, '-')
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(stopID[i+1:])
	if err != nil {
		return "", 0, false
	}
	return stopID[:i], n, true
}

func (a *dayAcc) add(r idxRow) {
	run, idx, ok := splitStopID(r.StopID)
	if !ok || len(run) < 11 || run[len(run)-10:len(run)-4] != a.dayPat {
		return // other service day (post-midnight remnant) or broken id
	}
	label := r.LineLabel
	if label == "" {
		label = r.TrainNo // like labelExpr
	}
	t := a.trips[run]
	if t == nil {
		t = &accTrip{stops: map[int]accStop{}}
		a.trips[run] = t
	}
	// tl-less fchg rows carry a category guessed from `l` ("RE"+"RE7"
	// instead of "NX"+"RE7") — rows with <tl> take precedence.
	if r.Cat != "" && label != "" && (t.cat == "" || (r.HasTL && !t.hasTL)) {
		t.cat, t.label, t.hasTL = r.Cat, label, r.HasTL
		if r.TrainNo != "" {
			t.trainNo = r.TrainNo
		}
	}
	if t.trainNo == "" && r.TrainNo != "" && r.HasTL {
		t.trainNo = r.TrainNo
	}
	dep := r.Direction == "departure"
	if s, exists := t.stops[idx]; !exists || (dep && !s.dep) {
		t.stops[idx] = accStop{eva: r.Eva, min: minutesOfDay(r.Planned, a.anchor), dep: dep}
	}
}

func catOrder(cat string) int {
	switch strings.ToUpper(cat) {
	case "S":
		return 0
	case "RB":
		return 1
	case "RE":
		return 2
	case "IRE":
		return 3
	case "IC":
		return 4
	case "EC":
		return 5
	case "ICE":
		return 6
	}
	return 7
}

// leadingInt: leading number of a label for numeric sorting.
func leadingInt(s string) int {
	n, seen := 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
			seen = true
			if n > 1e8 {
				break
			}
		} else if seen {
			break
		}
	}
	if !seen {
		return 1 << 30
	}
	return n
}

// lineLess: deterministic order on ties.
func lineLess(a, b *idxLine) bool {
	if oa, ob := catOrder(a.Cat), catOrder(b.Cat); oa != ob {
		return oa < ob
	}
	if na, nb := leadingInt(a.Label), leadingInt(b.Label); na != nb {
		return na < nb
	}
	return a.Key < b.Key
}

var defaultCats = []string{"ice", "ic", "ec", "ece", "en", "nj", "rj", "rjx", "tgv", "flx",
	"ire", "re", "rb", "s", "rs", "mex", "me", "bus", "nx"}

// snapshot builds an immutable index from the accumulator (ms at ~35k runs).
func (a *dayAcc) snapshot(day string, start, end time.Time) *dayIndex {
	di := &dayIndex{
		Day: day, Start: start, End: end, Built: time.Now(),
		byKey:        map[string]*idxLine{},
		Trips:        make(map[string]*idxTrip, len(a.trips)),
		byTrainNo:    map[string][]*idxTrip{},
		stationLines: map[string][]*idxLine{},
		stationTrips: map[string]int{},
		cats:         map[string]bool{},
	}
	for _, c := range defaultCats {
		di.cats[c] = true
	}
	// Runs known only from tl-less fchg rows (category guessed from `l`:
	// "RS"+"RS12" instead of "SWE"+"RS12") are assigned to the <tl> line
	// with the same label category+number (ties: the one with the most
	// runs). Otherwise they form their own line.
	runs := make([]string, 0, len(a.trips))
	for run := range a.trips {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { // <tl> runs first, then deterministic
		ti, tj := a.trips[runs[i]], a.trips[runs[j]]
		if ti.hasTL != tj.hasTL {
			return ti.hasTL
		}
		return runs[i] < runs[j]
	})
	byEff := map[string]*idxLine{}
	for _, run := range runs {
		at := a.trips[run]
		if at.cat == "" || at.label == "" || len(at.stops) == 0 {
			continue
		}
		key := lineKeyOf(at.cat, at.label)
		l := di.byKey[key]
		if !at.hasTL {
			cand := newLine(at.cat, at.label)
			if tl := byEff[cand.effCat+"|"+cand.num]; tl != nil && cand.num != "" {
				l = tl
			}
		}
		if l == nil {
			l = newLine(at.cat, at.label)
			di.byKey[key] = l
			di.Lines = append(di.Lines, l)
		}
		if at.hasTL {
			ek := l.effCat + "|" + l.num
			if cur := byEff[ek]; cur == nil || len(l.Trips) >= len(cur.Trips) {
				byEff[ek] = l
			}
		}
		t := &idxTrip{Run: run, Line: l, TrainNo: at.trainNo, Stops: make([]idxStop, 0, len(at.stops))}
		for idx, s := range at.stops {
			t.Stops = append(t.Stops, idxStop{Eva: s.eva, Idx: idx, Min: s.min, Dep: s.dep})
		}
		sort.Slice(t.Stops, func(i, j int) bool { return t.Stops[i].Idx < t.Stops[j].Idx })
		l.Trips = append(l.Trips, t)
		di.Trips[run] = t
		if t.TrainNo != "" {
			n := strings.TrimLeft(t.TrainNo, "0")
			di.byTrainNo[n] = append(di.byTrainNo[n], t)
		}
		seen := map[string]bool{}
		for _, s := range t.Stops {
			l.Stations[s.Eva]++
			if !seen[s.Eva] {
				seen[s.Eva] = true
				di.stationTrips[s.Eva]++
			}
		}
	}
	for _, l := range di.Lines {
		sort.Slice(l.Trips, func(i, j int) bool {
			if l.Trips[i].Stops[0].Min != l.Trips[j].Stops[0].Min {
				return l.Trips[i].Stops[0].Min < l.Trips[j].Stops[0].Min
			}
			return l.Trips[i].Run < l.Trips[j].Run
		})
		first := l.Trips[0]
		l.FromEva, l.ToEva = first.Stops[0].Eva, first.Stops[len(first.Stops)-1].Eva
		di.cats[l.catN] = true
		di.cats[l.effCat] = true
		for eva := range l.Stations {
			di.stationLines[eva] = append(di.stationLines[eva], l)
		}
	}
	sort.Slice(di.Lines, func(i, j int) bool { return lineLess(di.Lines[i], di.Lines[j]) })
	for eva, ls := range di.stationLines {
		sort.Slice(ls, func(i, j int) bool {
			if ni, nj := ls[i].Stations[eva], ls[j].Stations[eva]; ni != nj {
				return ni > nj
			}
			return lineLess(ls[i], ls[j])
		})
	}
	for _, ts := range di.byTrainNo {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Stops[0].Min < ts[j].Stops[0].Min })
	}
	return di
}

// ---------------------------------------------------------------------------
// Day index store (lazy, one immutable build per day, LRU)
// ---------------------------------------------------------------------------

const (
	maxDayIndexes    = 4                // days held in RAM (LRU)
	indexRetry       = time.Minute      // earliest retry after a failed build
	indexLead        = 2 * time.Hour    // plan rows from the previous evening for early runs
	indexFirstWait   = 4 * time.Second  // how long the first search waits
	indexBuildBudget = 15 * time.Minute // overall budget per build
)

// dayIndexSQL reads the raw index rows of one day window; the relation is
// filled in by the caller (store.StopEvents covers the previous evening).
const dayIndexSQL = `
	SELECT stop_id, station_eva, direction, line_category,
	       COALESCE(line_label, ''), COALESCE(train_number, ''),
	       (owner IS NOT NULL OR trip_kind IS NOT NULL) AS has_tl,
	       planned_time
	FROM %s
	WHERE scraped_at >= $1 AND scraped_at < $2
	  AND line_category <> ''`

type dayIndexHolder struct {
	snap atomic.Pointer[dayIndex]

	// guarded by searchStore.mu:
	building  bool
	nextBuild time.Time // earliest next build (retry rhythm)
	lastUsed  time.Time
	done      chan struct{} // closed when the running build ends
	complete  bool          // day fully read
}

type searchStore struct {
	mu   sync.Mutex
	days map[string]*dayIndexHolder
}

// dayIndexFor returns the current snapshot (nil while the first build is
// running) and starts exactly one background build if needed. Only the very
// first request for a day waits (indexFirstWait); it answers pending=true
// when the build has not finished by then.
func (d *dataUI) dayIndexFor(ctx context.Context, start, end time.Time) (*dayIndex, bool) {
	key := start.Format("2006-01-02")
	now := time.Now()
	s := &d.search
	s.mu.Lock()
	if s.days == nil {
		s.days = map[string]*dayIndexHolder{}
	}
	h := s.days[key]
	if h == nil {
		if len(s.days) >= maxDayIndexes {
			s.evictLocked()
		}
		h = &dayIndexHolder{}
		s.days[key] = h
	}
	h.lastUsed = now
	if !h.building && !h.complete && !now.Before(h.nextBuild) {
		h.building = true
		h.nextBuild = now.Add(indexRetry) // no periodic refresh: just guard retries
		h.done = make(chan struct{})
		go d.buildDayIndex(h, key, start, end)
	}
	building, done := h.building, h.done
	s.mu.Unlock()

	snap := h.snap.Load()
	if snap == nil && building {
		t := time.NewTimer(indexFirstWait)
		select {
		case <-done:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
		snap = h.snap.Load()
	}
	return snap, snap == nil
}

func (s *searchStore) evictLocked() {
	oldKey, oldAt := "", time.Time{}
	for k, h := range s.days {
		if h.building {
			continue
		}
		if oldKey == "" || h.lastUsed.Before(oldAt) {
			oldKey, oldAt = k, h.lastUsed
		}
	}
	if oldKey != "" {
		delete(s.days, oldKey)
	}
}

// buildDayIndex reads the day's rows once and publishes the snapshot. On
// error no snapshot is published and the build is retried later.
func (d *dataUI) buildDayIndex(h *dayIndexHolder, key string, start, end time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), indexBuildBudget)
	defer cancel()
	release, err := d.acquireHeavy(ctx, 2*time.Minute) // background: may wait
	if err == nil {
		defer release()
	}
	began := time.Now()
	acc := newDayAcc(start)
	rows := 0
	complete := false
	if err == nil {
		// Window: from the previous evening (early runs' plan rows) to the
		// end of the day; file window covers the previous day automatically.
		from := start.Add(-indexLead)
		q := fmt.Sprintf(dayIndexSQL, d.st.StopEvents(start.AddDate(0, 0, -1), end))
		var rerr error
		rows, rerr = func() (int, error) {
			rs, err := d.st.DB.QueryContext(ctx, q, from, end)
			if err != nil {
				return 0, err
			}
			defer rs.Close()
			n := 0
			for rs.Next() {
				var r idxRow
				if err := rs.Scan(&r.StopID, &r.Eva, &r.Direction, &r.Cat, &r.LineLabel, &r.TrainNo, &r.HasTL, &r.Planned); err != nil {
					return n, err
				}
				acc.add(r)
				n++
			}
			return n, rs.Err()
		}()
		if rerr != nil {
			err = rerr
		}
	}
	if err != nil {
		log.Printf("[search] index %s: %v", key, err)
	} else {
		h.snap.Store(acc.snapshot(key, start, end))
		complete = true
	}
	log.Printf("[search] index %s: %d rows, %d trips, %s (err=%v)",
		key, rows, len(acc.trips), time.Since(began).Round(time.Millisecond), err != nil)

	d.search.mu.Lock()
	h.building = false
	h.complete = complete
	if err != nil {
		h.nextBuild = time.Now().Add(indexRetry)
	}
	close(h.done)
	d.search.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Query parser
// ---------------------------------------------------------------------------

// parsedQuery: roles of the query words.
//
// Rules (left to right):
//   - Time: "14:30", "14.30", "14h30", "14 uhr", "14 uhr 30", "um 14",
//     "um 1430", "1430 uhr". A BARE four-digit number (0000–2359) counts as
//     a time only when no category precedes it and the rest of the query
//     hits a station ("köln 1830", "horrem s12 1430"); otherwise it is a
//     train/line number ("IC 2010", "1918", "ICE 1530").
//   - Category+number: a category word directly followed by a number, also
//     across spaces ("RE 7" = "re7"); letters+digits in ONE token always
//     count as category+number ("rs12", "xyz5").
//   - Category alone ("ice", "köln ice") → category filter.
//   - Number without category: 1–2 digits = line label; ≥3 digits
//     additionally a train number.
//   - Filler words are ignored (von, ab, um, uhr, gleis+number, zug,
//     linie); "nach/richtung/ri/bis X" makes X a destination hint.
//   - everything else → station phrase.
type parsedQuery struct {
	station []string // words of the station phrase
	dest    []string // words after "nach/richtung"
	cat     string   // normalized category (with or without number)
	op      string   // category word directly before category+number ("nx" in "NX RE7")
	num     string   // number (line label / train number)
	catOnly bool     // category without number
	time    int
	hasTime bool
}

var (
	reTimeSep  = regexp.MustCompile(`(?:^|\s)(?:um\s+)?([01]?\d|2[0-3])\s*[:.h]\s*([0-5]\d)(?:\s*uhr)?(?:\s|$)`)
	reTimeUhr  = regexp.MustCompile(`(?:^|\s)(?:um\s+)?([01]?\d|2[0-3])([0-5]\d)?\s*uhr(?:\s+([0-5]\d))?(?:\s|$)`)
	reTimeUm   = regexp.MustCompile(`(?:^|\s)um\s+([01]?\d|2[0-3])([0-5]\d)?(?:\s|$)`)
	destWords  = map[string]bool{"nach": true, "richtung": true, "ri": true, "bis": true}
	origWords  = map[string]bool{"von": true, "ab": true}
	fillWords  = map[string]bool{"um": true, "uhr": true, "zug": true, "linie": true, "fahrt": true}
	gleisWords = map[string]bool{"gleis": true, "gl": true}
)

func extractTime(q string) (string, int, bool) {
	for _, re := range []*regexp.Regexp{reTimeSep, reTimeUhr, reTimeUm} {
		m := re.FindStringSubmatchIndex(q)
		if m == nil {
			continue
		}
		h, _ := strconv.Atoi(q[m[2]:m[3]])
		mm := 0
		for g := 2; g*2+1 < len(m); g++ {
			if m[g*2] >= 0 {
				mm, _ = strconv.Atoi(q[m[g*2]:m[g*2+1]])
				break
			}
		}
		return strings.TrimSpace(q[:m[0]] + " " + q[m[1]:]), h*60 + mm, true
	}
	return q, 0, false
}

type qWord struct {
	w     string
	joint bool // letters+digits in the same token ("s12")
}

// parseQuery decomposes the query. isStation checks whether a word sequence
// hits a station (for the bare four-digit-number time rule).
func parseQuery(q string, cats map[string]bool, isStation func([]string) bool) parsedQuery {
	var pq parsedQuery
	rest, t, ok := extractTime(strings.ToLower(q))
	if ok {
		pq.time, pq.hasTime = t, true
	}
	var ws []qWord
	for _, tok := range strings.Fields(rest) {
		parts := foldWords(tok, false)
		for i, p := range parts {
			joint := len(parts) > 1 && ((i+1 < len(parts) && isLetters(p) && isDigits(parts[i+1])) ||
				(i > 0 && isDigits(p) && isLetters(parts[i-1])))
			ws = append(ws, qWord{w: p, joint: joint})
		}
	}

	mode := "station"
	var catCands, bareNums []string
	catBeforeNum := map[int]bool{} // index in bareNums with a category before it
	for i := 0; i < len(ws); i++ {
		w := ws[i].w
		switch {
		case destWords[w]:
			mode = "dest"
			continue
		case origWords[w]:
			mode = "station"
			continue
		case fillWords[w]:
			continue
		case gleisWords[w]:
			if i+1 < len(ws) && isDigits(ws[i+1].w) {
				i++
			}
			continue
		}
		if isLetters(w) && i+1 < len(ws) && isDigits(ws[i+1].w) && (cats[w] || (ws[i].joint && len(w) <= 4)) {
			if pq.cat == "" && pq.num == "" {
				pq.cat, pq.num = w, strings.TrimLeft(ws[i+1].w, "0")
				if pq.num == "" {
					pq.num = "0"
				}
				if n := len(catCands); n > 0 && i > 0 && ws[i-1].w == catCands[n-1] {
					pq.op = catCands[n-1] // operator prefix, not a category filter
					catCands = catCands[:n-1]
				}
			} else {
				catBeforeNum[len(bareNums)] = true
				bareNums = append(bareNums, ws[i+1].w)
			}
			i++
			continue
		}
		if isLetters(w) && cats[w] {
			catCands = append(catCands, w)
			continue
		}
		if isDigits(w) {
			bareNums = append(bareNums, w)
			continue
		}
		if mode == "dest" {
			pq.dest = append(pq.dest, w)
		} else {
			pq.station = append(pq.station, w)
		}
	}

	for i, n := range bareNums {
		if !pq.hasTime && !catBeforeNum[i] && len(n) == 4 && validHHMM(n) &&
			len(pq.station) > 0 && isStation(pq.station) {
			h, _ := strconv.Atoi(n[:2])
			m, _ := strconv.Atoi(n[2:])
			pq.time, pq.hasTime = h*60+m, true
			continue
		}
		if pq.num == "" && !catBeforeNum[i] {
			pq.num = strings.TrimLeft(n, "0")
			if pq.num == "" {
				pq.num = "0"
			}
		}
	}

	if pq.cat == "" && len(catCands) > 0 {
		pq.cat = catCands[0]
		if pq.num == "" {
			pq.catOnly = true
		}
		// "re" / "ice" alone could also be a station prefix — only treat it
		// as a category when there are no other station words.
		if len(pq.station) == 0 && len(pq.dest) == 0 && len(catCands[0]) >= 2 {
			pq.station = []string{catCands[0]}
		}
	}
	return pq
}

func validHHMM(n string) bool {
	h, _ := strconv.Atoi(n[:2])
	m, _ := strconv.Atoi(n[2:])
	return h <= 23 && m <= 59
}

// ---------------------------------------------------------------------------
// Line matching
// ---------------------------------------------------------------------------

// Scores (lower = better), rank first, cut ONCE at the end:
//
//	 0  operator+category+number exact ("NX RE7" → NX|RE7)
//	10  category+number exact ("RE 7" → RE|7; "s5" → S|S5)
//	15  category via the label only (operator prefix): "re7" → NX|RE7
//	20  number only, label exact ("7" → RE 7, EC 7)
//	25  number only, label exact with operator prefix ("7" → NX RE7)
//	30  category+number as prefix ("RE 7" → RE 70) / label word exact ("sev")
//	40  number only, prefix ("57" → ICE 579)
//	50  category only ("ice")
//
// Ties: more runs first, then category order, then numerically by label,
// then key (deterministic).
type lineHit struct {
	l     *idxLine
	score int
}

func matchLines(di *dayIndex, pq parsedQuery) []lineHit {
	if di == nil {
		return nil
	}
	var word string // single letter word without category/number ("sev")
	if pq.cat == "" && pq.num == "" && len(pq.station) == 1 && isLetters(pq.station[0]) {
		word = pq.station[0]
	}
	var hits []lineHit
	for _, l := range di.Lines {
		s := -1
		switch {
		case pq.cat != "" && pq.num != "":
			sameCat := l.catN == pq.cat
			viaLabel := l.effCat == pq.cat
			switch {
			case pq.op != "" && l.catN == pq.op && viaLabel && l.num == pq.num:
				s = 0
			case sameCat && (l.labelN == pq.num || (viaLabel && l.num == pq.num)):
				s = 10
			case !sameCat && viaLabel && l.num == pq.num:
				s = 15
			case (sameCat || viaLabel) && l.num != pq.num && strings.HasPrefix(l.num, pq.num):
				s = 30
			}
		case pq.num != "":
			switch {
			case l.num == pq.num && l.effCat == l.catN:
				s = 20
			case l.num == pq.num:
				s = 25
			case strings.HasPrefix(l.num, pq.num):
				s = 40
			}
		case pq.catOnly:
			if l.catN == pq.cat || l.effCat == pq.cat {
				s = 50
			}
		case word != "":
			if l.labelN == word || l.fullN == word {
				s = 30
			}
		}
		if s >= 0 {
			hits = append(hits, lineHit{l, s})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.score != b.score {
			return a.score < b.score
		}
		if len(a.l.Trips) != len(b.l.Trips) {
			return len(a.l.Trips) > len(b.l.Trips)
		}
		return lineLess(a.l, b.l)
	})
	return hits
}

// ---------------------------------------------------------------------------
// Search (pure function — tested without a DB)
// ---------------------------------------------------------------------------

func hhmm(min int) string {
	m := ((min % 1440) + 1440) % 1440
	return strconv.Itoa(m/600) + strconv.Itoa(m/60%10) + ":" + strconv.Itoa(m%60/10) + strconv.Itoa(m%10)
}

func runSearch(st *stationIndex, di *dayIndex, q string, stationLimit int) uiSearchResults {
	out := uiSearchResults{Stations: []uiSearchStation{}, Lines: []uiSearchLine{}}
	var weight map[string]int
	cats := map[string]bool{}
	for _, c := range defaultCats {
		cats[c] = true
	}
	if di != nil {
		weight, cats = di.stationTrips, di.cats
	}
	pq := parseQuery(q, cats, func(ws []string) bool { return len(st.match(ws, nil)) > 0 })
	if pq.hasTime {
		t := pq.time
		out.Time = &t
	}

	// Stations
	stHits := st.match(pq.station, weight)
	var destHits []stationHit
	if len(pq.dest) > 0 {
		destHits = st.match(pq.dest, weight)
		if len(stHits) == 0 && len(pq.station) == 0 {
			stHits = destHits // "nach Aachen": show Aachen (not Eisenach)
		}
	}
	for _, h := range stHits {
		if len(out.Stations) >= stationLimit {
			break
		}
		e := &st.list[h.i]
		out.Stations = append(out.Stations, uiSearchStation{Name: e.Name, N: weight[e.Eva]})
	}
	if di == nil {
		return out
	}
	destEvas := map[string]bool{}
	for i, h := range destHits {
		if i >= 3 {
			break
		}
		destEvas[st.list[h.i].Eva] = true
	}
	stopsAtDest := func(l *idxLine) bool {
		if len(destEvas) == 0 {
			return true
		}
		for eva := range destEvas {
			if l.Stations[eva] > 0 {
				return true
			}
		}
		return false
	}

	// Lines (the whole ranked list; cut only for output)
	var lines []lineHit
	for _, h := range matchLines(di, pq) {
		if stopsAtDest(h.l) {
			lines = append(lines, h)
		}
	}
	for _, h := range lines {
		if len(out.Lines) >= maxLines {
			break
		}
		out.Lines = append(out.Lines, searchLine(st, h.l, ""))
	}

	// Line at station: the first (best-ranked) station where at least one
	// of the found lines stops according to the index.
	var lasEva string
	var lasLines []*idxLine
	if len(lines) > 0 && len(stHits) > 0 && len(pq.station) > 0 && !(pq.catOnly && len(pq.station) == 1 && pq.station[0] == pq.cat) {
		for i, h := range stHits {
			if i >= maxStations {
				break
			}
			eva := st.list[h.i].Eva
			for _, lh := range lines {
				if lh.l.Stations[eva] > 0 && len(lasLines) < maxLineAtStation {
					lasLines = append(lasLines, lh.l)
				}
			}
			if len(lasLines) > 0 {
				lasEva = eva
				break
			}
		}
		for _, l := range lasLines {
			out.LineAtStation = append(out.LineAtStation, uiLineAtStation{
				Line:    uiLineInfo{Key: l.Key, Label: lineLabelOf(l.Cat, l.Label), Cat: l.Cat},
				Station: st.nameOf(lasEva),
			})
		}
	}

	// Runs
	seenTrip := map[string]bool{}
	addTrip := func(t *idxTrip, atEva string) {
		if len(out.Trips) >= maxTrips || seenTrip[t.Run] {
			return
		}
		seenTrip[t.Run] = true
		out.Trips = append(out.Trips, searchTrip(st, di, t, atEva))
	}
	// (a) line + station + time → next departures from that time
	if pq.hasTime && lasEva != "" {
		type cand struct {
			t   *idxTrip
			min int
		}
		var cs []cand
		for _, l := range lasLines {
			for _, t := range l.Trips {
				s, ok := t.stopAt(lasEva)
				if !ok || !s.Dep || s.Min < pq.time { // departures only (not the terminus)
					continue
				}
				if len(destEvas) > 0 && !reachesAfter(t, s.Idx, destEvas) {
					continue
				}
				cs = append(cs, cand{t, s.Min})
			}
		}
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].min != cs[j].min {
				return cs[i].min < cs[j].min
			}
			return cs[i].t.Run < cs[j].t.Run
		})
		for _, c := range cs {
			addTrip(c.t, lasEva)
		}
	}
	// (b) train number (≥3 digits), optionally with a category; for long-
	// distance the label IS the number → the exact line with few runs is
	// also offered as a trip.
	if len(pq.num) >= 3 {
		var ts []*idxTrip
		for _, t := range di.byTrainNo[pq.num] {
			if pq.cat == "" || t.Line.catN == pq.cat || t.Line.effCat == pq.cat {
				ts = append(ts, t)
			}
		}
		for _, h := range lines {
			if h.score <= 25 && len(h.l.Trips) <= 3 {
				ts = append(ts, h.l.Trips...)
			}
		}
		for _, t := range ts {
			at := ""
			for i, h := range stHits {
				if i >= maxStations {
					break
				}
				if _, ok := t.stopAt(st.list[h.i].Eva); ok {
					at = st.list[h.i].Eva
					break
				}
			}
			addTrip(t, at)
		}
	}

	// Lines at the strongest station hit (most frequent first — not by
	// category, otherwise Köln Hbf would only ever show IC/ICE).
	if len(out.Stations) > 0 {
		eva := st.list[stHits[0].i].Eva
		out.StationName = out.Stations[0].Name
		for _, l := range di.stationLines[eva] {
			if len(out.StationLines) >= maxStationLines {
				break
			}
			out.StationLines = append(out.StationLines, uiSearchLine{
				Key: l.Key, Label: lineLabelOf(l.Cat, l.Label), Cat: l.Cat,
				Ctx: "hält hier", N: l.Stations[eva],
			})
		}
	}
	return out
}

func reachesAfter(t *idxTrip, idx int, evas map[string]bool) bool {
	for _, s := range t.Stops {
		if s.Idx > idx && evas[s.Eva] {
			return true
		}
	}
	return false
}

func searchLine(st *stationIndex, l *idxLine, ctx string) uiSearchLine {
	if ctx == "" {
		ctx = st.nameOf(l.FromEva) + " ⇢ " + st.nameOf(l.ToEva)
	}
	return uiSearchLine{Key: l.Key, Label: lineLabelOf(l.Cat, l.Label), Cat: l.Cat, Ctx: ctx, N: len(l.Trips)}
}

func searchTrip(st *stationIndex, di *dayIndex, t *idxTrip, atEva string) uiSearchTrip {
	first, last := t.Stops[0], t.Stops[len(t.Stops)-1]
	out := uiSearchTrip{
		RunID: t.Run, Day: di.Day, Key: t.Line.Key,
		Label: lineLabelOf(t.Line.Cat, t.Line.Label), Cat: t.Line.Cat,
		Number: t.TrainNo, From: st.nameOf(first.Eva), To: st.nameOf(last.Eva),
		Dep: hhmm(first.Min),
	}
	if out.Number == "" {
		out.Number = t.Line.Label
	}
	if atEva != "" {
		if s, ok := t.stopAt(atEva); ok {
			out.Station, out.StopIdx, out.Dep = st.nameOf(atEva), s.Idx, hhmm(s.Min)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// handleSearch: GET /api/search?q=&day=[&kind=stations]
// kind=stations (board combobox) returns only stations (max. 10) and never
// triggers a day index build.
func (d *dataUI) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		q = q[:120]
	}
	start, end, _, ok := d.dayWindow(w, r)
	if !ok {
		return
	}
	if q == "" {
		writeJSONCached(w, r, ageData, uiSearchResults{Stations: []uiSearchStation{}, Lines: []uiSearchLine{}})
		return
	}
	st := d.stations.get(r.Context())
	limit := maxStations
	var di *dayIndex
	pending := false
	if r.URL.Query().Get("kind") == "stations" {
		limit = maxStationsBoard
	} else {
		di, pending = d.dayIndexFor(r.Context(), start, end)
	}
	if st == nil {
		st = newStationIndex(nil, time.Time{}) // DB gone: lines still work
		pending = true
	}
	res := runSearch(st, di, q, limit)
	res.Pending = pending
	age := ageData
	if pending {
		age = 0
	}
	writeJSONCached(w, r, age, res)
}
