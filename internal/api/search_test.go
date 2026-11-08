package api

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Fixture: stations in StaDa style (with category) and a day index built
// from synthetic index rows (going through dayAcc.add → snapshot, so the
// index build is covered too). No DB access.

var fixtureStations = []struct {
	name string
	cat  int
}{
	{"Köln Hbf", 1}, {"Köln Messe/Deutz", 2}, {"Köln-Mülheim", 3}, {"Köln Süd", 4}, {"Köln West", 4},
	{"Köln-Ehrenfeld", 3}, {"Köln/Bonn Flughafen", 2}, {"Porz(Rhein)", 4}, {"Köln Frankfurter Straße", 5},
	{"Kölln-Reisiek", 6}, {"Düsseldorf Hbf", 1}, {"Düsseldorf Flughafen", 2}, {"Düsseldorf-Benrath", 3},
	{"Düsseldorf-Bilk", 4}, {"Düren", 2}, {"Dülmen", 4}, {"Duisburg Hbf", 1}, {"Dortmund Hbf", 1},
	{"Gießen", 2}, {"Gießen Oswaldsgarten", 5}, {"München Hbf", 1}, {"München Ost", 2}, {"München-Pasing", 2},
	{"München Flughafen Terminal", 3}, {"Frankfurt (Main) Hbf", 1}, {"Frankfurt (Main) Süd", 2},
	{"Frankfurt (Main) West", 3}, {"Frankfurt (Main) Flughafen Fernbahnhof", 1}, {"Frankfurt (Oder)", 2},
	{"Frankfurt-Höchst", 3}, {"Berlin Hbf", 1}, {"Berlin Ostbahnhof", 1}, {"Berlin-Spandau", 2},
	{"Berlin Zoologischer Garten", 2}, {"Berlin Südkreuz", 1}, {"Berlin Gesundbrunnen", 1},
	{"Hamburg Hbf", 1}, {"Hamburg-Altona", 1}, {"Hamburg Dammtor", 2}, {"Hamburg-Harburg", 2},
	{"Horrem", 3}, {"Aachen Hbf", 2}, {"Aachen West", 4}, {"Eisenach", 3}, {"Hof Hbf", 3}, {"Ulm Hbf", 2},
	{"Essen Hbf", 1}, {"Münster (Westf) Hbf", 2}, {"Bad Homburg", 3}, {"Bad Oeynhausen", 3}, {"Bad Ems", 5},
	{"Kassel-Wilhelmshöhe", 1}, {"Kassel Hbf", 2}, {"Mainz Hbf", 1}, {"Bonn Hbf", 2}, {"Siegburg/Bonn", 2},
	{"Soest", 3}, {"St. Wendel", 4}, {"Sankt Goarshausen", 5}, {"St Goar", 5}, {"Halle (Saale) Hbf", 2},
	{"Offenbach (Main) Hbf", 3}, {"Weil am Rhein", 4}, {"Krefeld Hbf", 2}, {"Rheine", 3}, {"Rees", 5},
	{"Nachrodt", 6}, {"Au (Sieg)", 4}, {"Hennef (Sieg)", 4}, {"Troisdorf", 3}, {"Kerpen-Sindorf", 5},
	{"Buir", 5}, {"Mönchengladbach Hbf", 2}, {"Regensburg Hbf", 2}, {"Ingolstadt Hbf", 2},
	{"Neuss Hbf", 3}, {"Hamm (Westf) Hbf", 2}, {"Stuttgart Hbf", 1}, {"Wiesbaden Hbf", 2},
}

var fixtureDay = time.Date(2026, 10, 30, 0, 0, 0, 0, berlinLoc)

func fixtureStationIndex() *stationIndex {
	var list []stationEntry
	for i, s := range fixtureStations {
		list = append(list, stationEntry{Eva: fmt.Sprintf("80%05d", i), Name: s.name, Cat: s.cat})
	}
	return newStationIndex(list, time.Now())
}

type fxLine struct {
	cat, label string
	trainNo    func(i int) string // tl.n per run
	stops      []string
	trips      int
	firstDep   int // minute of the first departure
	every      int // headway in minutes
	noTL       bool
}

func fixtureDayIndex(st *stationIndex) *dayIndex {
	num := func(n string) func(int) string { return func(int) string { return n } }
	seq := func(base int) func(int) string { return func(i int) string { return fmt.Sprint(base + i) } }
	lines := []fxLine{
		{cat: "S", label: "12", trainNo: seq(38100), stops: []string{"Horrem", "Köln Hbf", "Köln Messe/Deutz", "Porz(Rhein)", "Troisdorf", "Hennef (Sieg)", "Au (Sieg)"}, trips: 40, firstDep: 290, every: 20},
		{cat: "S", label: "11", trainNo: seq(39100), stops: []string{"Düsseldorf Hbf", "Neuss Hbf", "Köln Hbf", "Köln Messe/Deutz", "Köln-Mülheim"}, trips: 30, firstDep: 300, every: 20},
		{cat: "S", label: "6", trainNo: seq(30600), stops: []string{"Essen Hbf", "Düsseldorf Hbf", "Köln Hbf", "Köln-Mülheim"}, trips: 30, firstDep: 300, every: 20},
		{cat: "S", label: "1", trainNo: seq(30100), stops: []string{"Dortmund Hbf", "Essen Hbf", "Duisburg Hbf"}, trips: 20, firstDep: 300, every: 30},
		{cat: "S", label: "13", trainNo: seq(31300), stops: []string{"Köln Hbf", "Troisdorf"}, trips: 20, firstDep: 300, every: 30},
		{cat: "S", label: "8", trainNo: seq(30800), stops: []string{"Wiesbaden Hbf", "Mainz Hbf", "Frankfurt (Main) Hbf", "Offenbach (Main) Hbf"}, trips: 20, firstDep: 300, every: 30},
		{cat: "S", label: "S5", trainNo: seq(30500), stops: []string{"Halle (Saale) Hbf", "Halle (Saale) Hbf"}, trips: 4, firstDep: 300, every: 60},
		{cat: "RE", label: "7", trainNo: seq(10123), stops: []string{"Krefeld Hbf", "Neuss Hbf", "Köln Hbf", "Köln Messe/Deutz", "Troisdorf"}, trips: 6, firstDep: 320, every: 60},
		{cat: "NX", label: "RE7", trainNo: seq(10200), stops: []string{"Krefeld Hbf", "Neuss Hbf", "Köln Hbf", "Köln Messe/Deutz", "Troisdorf"}, trips: 20, firstDep: 350, every: 60},
		{cat: "RE", label: "70", trainNo: seq(10700), stops: []string{"Hamm (Westf) Hbf", "Soest"}, trips: 8, firstDep: 300, every: 60},
		{cat: "RE", label: "17", trainNo: seq(11700), stops: []string{"Kassel Hbf", "Hamm (Westf) Hbf"}, trips: 8, firstDep: 300, every: 60},
		{cat: "RE", label: "1", trainNo: seq(13100), stops: []string{"Aachen Hbf", "Düren", "Horrem", "Köln Hbf", "Düsseldorf Hbf"}, trips: 30, firstDep: 300, every: 30},
		{cat: "RE", label: "12", trainNo: seq(11200), stops: []string{"Köln Hbf", "Horrem"}, trips: 10, firstDep: 300, every: 60},
		{cat: "RB", label: "25", trainNo: seq(12500), stops: []string{"Köln Hbf", "Köln-Mülheim"}, trips: 20, firstDep: 300, every: 60},
		{cat: "RB", label: "21", trainNo: seq(12100), stops: []string{"Düren", "Buir"}, trips: 20, firstDep: 300, every: 60},
		{cat: "RB", label: "12345", trainNo: num("12345"), stops: []string{"Rheine", "Münster (Westf) Hbf"}, trips: 1, firstDep: 400, every: 60},
		{cat: "SWE", label: "RS12", trainNo: seq(70000), stops: []string{"Rees", "Rheine"}, trips: 10, firstDep: 300, every: 60},
		{cat: "ERB", label: "RB44", trainNo: seq(71000), stops: []string{"Dortmund Hbf", "Soest"}, trips: 10, firstDep: 300, every: 60},
		{cat: "ICE", label: "579", trainNo: num("579"), stops: []string{"Dortmund Hbf", "Köln Hbf", "Frankfurt (Main) Flughafen Fernbahnhof", "Frankfurt (Main) Hbf", "München Hbf"}, trips: 1, firstDep: 600, every: 60},
		{cat: "ICE", label: "1579", trainNo: num("1579"), stops: []string{"Berlin Hbf", "Hamburg Hbf"}, trips: 1, firstDep: 700, every: 60},
		{cat: "ICE", label: "57", trainNo: num("57"), stops: []string{"Hamburg Hbf", "Berlin Hbf"}, trips: 1, firstDep: 700, every: 60},
		{cat: "ICE", label: "1067", trainNo: num("1067"), stops: []string{"Hamburg Hbf", "Köln Hbf"}, trips: 1, firstDep: 400, every: 60},
		{cat: "ICE", label: "1530", trainNo: num("1530"), stops: []string{"Hamburg Hbf", "Berlin Hbf"}, trips: 1, firstDep: 800, every: 60},
		{cat: "IC", label: "2010", trainNo: num("2010"), stops: []string{"Halle (Saale) Hbf", "Köln Hbf", "Bonn Hbf"}, trips: 1, firstDep: 500, every: 60},
		{cat: "IC", label: "2011", trainNo: num("2011"), stops: []string{"Bonn Hbf", "Köln Hbf", "Halle (Saale) Hbf"}, trips: 1, firstDep: 600, every: 60},
		{cat: "IC", label: "1918", trainNo: num("1918"), stops: []string{"Stuttgart Hbf", "Köln Hbf"}, trips: 1, firstDep: 600, every: 60},
		{cat: "EC", label: "7", trainNo: num("7"), stops: []string{"Hamburg Hbf", "Köln Hbf"}, trips: 1, firstDep: 600, every: 60},
		{cat: "Bus", label: "SEV", trainNo: seq(90000), stops: []string{"Soest", "Hamm (Westf) Hbf"}, trips: 5, firstDep: 300, every: 60},
	}
	acc := newDayAcc(fixtureDay)
	evaOf := func(name string) string { return st.list[st.byName[name]].Eva }
	tripID := 1000
	for _, l := range lines {
		for i := 0; i < l.trips; i++ {
			tripID++
			dep := l.firstDep + i*l.every
			planned := fixtureDay.Add(time.Duration(dep) * time.Minute)
			run := fmt.Sprintf("%d-%s", tripID, planned.In(berlinLoc).Format("0601021504"))
			for si, name := range l.stops {
				pt := planned.Add(time.Duration(si*10) * time.Minute)
				dir := "departure"
				if si == len(l.stops)-1 {
					dir = "arrival"
				}
				acc.add(idxRow{StopID: fmt.Sprintf("%s-%d", run, si+1), Eva: evaOf(name), Direction: dir,
					Cat: l.cat, LineLabel: l.label, TrainNo: l.trainNo(i), HasTL: true, Planned: pt})
				// a tl-less fchg row with the category guessed from `l` must not override:
				if l.cat == "NX" {
					acc.add(idxRow{StopID: fmt.Sprintf("%s-%d", run, si+1), Eva: evaOf(name), Direction: dir,
						Cat: "RE", LineLabel: "RE7", TrainNo: "7", Planned: pt})
				}
			}
		}
	}
	// a run of another service day (post-midnight remnant) must be ignored:
	acc.add(idxRow{StopID: "999-2610292350-3", Eva: evaOf("Köln Hbf"), Direction: "departure",
		Cat: "RE", LineLabel: "99", TrainNo: "99999", HasTL: true, Planned: fixtureDay.Add(10 * time.Minute)})
	return acc.snapshot("2026-10-30", fixtureDay, fixtureDay.AddDate(0, 0, 1))
}

// expectation helpers — each string list is "must appear in this order as
// prefix" unless noted.
type searchCase struct {
	q string
	// expected prefixes of the result lists (nil = don't care, []string{} = empty)
	stations  []string
	lines     []string
	las       []string // "Label@Station"
	trips     []string // "Label Number HH:MM[@Station]"
	time      int      // -1 = no time expected
	stName    string
	notLines  []string // must not appear anywhere in lines
	notSt     []string // must not appear anywhere in stations
	maxSt     int      // at most this many stations (0 = don't care)
	stLinesHd []string // StationLines prefix
}

func prefixOK(got, want []string) bool {
	if want == nil {
		return true
	}
	if len(want) == 0 {
		return len(got) == 0
	}
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSearchQueries(t *testing.T) {
	st := fixtureStationIndex()
	di := fixtureDayIndex(st)

	cases := []searchCase{
		// --- stations: umlauts, ASCII, abbreviations
		{q: "köln", stations: []string{"Köln Hbf", "Köln Messe/Deutz", "Köln/Bonn Flughafen"}, time: -1, notSt: []string{"Kölln-Reisiek"}},
		{q: "koeln", stations: []string{"Köln Hbf", "Köln Messe/Deutz"}, time: -1},
		{q: "koln", stations: []string{"Köln Hbf", "Köln Messe/Deutz"}, time: -1},
		{q: "  KÖLN   HBF  ", stations: []string{"Köln Hbf"}, maxSt: 1, time: -1},
		{q: "KOELN", stations: []string{"Köln Hbf"}, time: -1},
		{q: "Köln Hbf", stations: []string{"Köln Hbf"}, maxSt: 1, time: -1, stName: "Köln Hbf"},
		{q: "Köln Hauptbahnhof", stations: []string{"Köln Hbf"}, maxSt: 1, time: -1},
		{q: "köln hbf.", stations: []string{"Köln Hbf"}, maxSt: 1, time: -1},
		{q: "kö", stations: []string{"Köln Hbf"}, time: -1},
		{q: "dü", stations: []string{"Düsseldorf Hbf"}, time: -1},
		{q: "düss", stations: []string{"Düsseldorf Hbf", "Düsseldorf Flughafen"}, time: -1},
		{q: "dusseldorf", stations: []string{"Düsseldorf Hbf"}, time: -1},
		{q: "duesseldorf", stations: []string{"Düsseldorf Hbf"}, time: -1},
		{q: "Düsseldorf Hbf", stations: []string{"Düsseldorf Hbf"}, maxSt: 1, time: -1},
		{q: "gießen", stations: []string{"Gießen", "Gießen Oswaldsgarten"}, time: -1},
		{q: "giessen", stations: []string{"Gießen", "Gießen Oswaldsgarten"}, time: -1},
		{q: "Giesen", stations: []string{"Gießen"}, time: -1},
		{q: "münchen", stations: []string{"München Hbf"}, time: -1},
		{q: "muenchen", stations: []string{"München Hbf"}, time: -1},
		{q: "munchen hbf", stations: []string{"München Hbf"}, maxSt: 1, time: -1},
		{q: "Frankfurt", stations: []string{"Frankfurt (Main) Hbf", "Frankfurt (Main) Flughafen Fernbahnhof"}, time: -1},
		{q: "Frankfurt Hbf", stations: []string{"Frankfurt (Main) Hbf"}, maxSt: 1, time: -1},
		{q: "Frankfurt(Main)Hbf", stations: []string{"Frankfurt (Main) Hbf"}, maxSt: 1, time: -1},
		{q: "frankfurt main", stations: []string{"Frankfurt (Main) Hbf"}, notSt: []string{"Mainz Hbf", "Offenbach (Main) Hbf", "Frankfurt (Oder)"}, time: -1},
		{q: "ffm", stations: []string{"Frankfurt (Main) Hbf"}, time: -1},
		{q: "frankfurt flughafen", stations: []string{"Frankfurt (Main) Flughafen Fernbahnhof"}, maxSt: 1, time: -1},
		{q: "St. Wendel", stations: []string{"St. Wendel"}, maxSt: 1, time: -1},
		{q: "sankt wendel", stations: []string{"St. Wendel"}, maxSt: 1, time: -1},
		{q: "st goar", stations: []string{"St Goar", "Sankt Goarshausen"}, notSt: []string{"Soest"}, time: -1},
		{q: "sankt goar", stations: []string{"St Goar", "Sankt Goarshausen"}, time: -1},
		{q: "Halle (Saale)", stations: []string{"Halle (Saale) Hbf"}, time: -1},
		{q: "halle saale", stations: []string{"Halle (Saale) Hbf"}, time: -1},
		{q: "Kassel-Wilhelmshöhe", stations: []string{"Kassel-Wilhelmshöhe"}, maxSt: 1, time: -1},
		{q: "kassel wilhelmshoehe", stations: []string{"Kassel-Wilhelmshöhe"}, maxSt: 1, time: -1},
		{q: "Kölm", stations: []string{"Köln Hbf"}, time: -1},
		{q: "Dusseldrof", stations: []string{"Düsseldorf Hbf"}, time: -1},
		{q: "Hambrug", stations: []string{"Hamburg Hbf"}, time: -1},
		{q: "berlin", stations: []string{"Berlin Hbf"}, time: -1},
		{q: "berlin hbf", stations: []string{"Berlin Hbf"}, maxSt: 1, time: -1},
		{q: "Berlin Zoo", stations: []string{"Berlin Zoologischer Garten"}, maxSt: 1, time: -1},
		{q: "hamburg altona", stations: []string{"Hamburg-Altona"}, maxSt: 1, time: -1},
		{q: "bad homburg", stations: []string{"Bad Homburg"}, maxSt: 1, time: -1},
		{q: "Köln Messe/Deutz", stations: []string{"Köln Messe/Deutz"}, maxSt: 1, time: -1},
		{q: "weil am rhein", stations: []string{"Weil am Rhein"}, maxSt: 1, time: -1},

		// --- lines and trains
		{q: "RE7", lines: []string{"RE 7", "NX RE7", "RE 70"}, notLines: []string{"RE 17"}, time: -1},
		{q: "re7", lines: []string{"RE 7", "NX RE7"}, time: -1},
		{q: "RE 7", lines: []string{"RE 7", "NX RE7", "RE 70"}, notLines: []string{"RE 1", "RE 17", "ICE 1067"}, time: -1, stations: []string{}},
		{q: "re 7", lines: []string{"RE 7", "NX RE7"}, time: -1, stations: []string{}},
		{q: "S12", lines: []string{"S 12"}, notLines: []string{"SWE RS12"}, time: -1},
		{q: "S 12", lines: []string{"S 12"}, notLines: []string{"S 1", "RE 12"}, stations: []string{}, time: -1},
		{q: "s 12", lines: []string{"S 12"}, stations: []string{}, time: -1},
		{q: "S 6", lines: []string{"S 6"}, stations: []string{}, time: -1},
		{q: "s6", lines: []string{"S 6"}, time: -1},
		{q: "S 1", lines: []string{"S 1", "S 12", "S 11", "S 13"}, time: -1},
		{q: "RB 25", lines: []string{"RB 25"}, notLines: []string{"RB 21"}, time: -1},
		{q: "rb25", lines: []string{"RB 25"}, time: -1},
		{q: "NX RE7", lines: []string{"NX RE7"}, time: -1},
		{q: "S5", lines: []string{"S 5"}, time: -1},
		{q: "RS12", lines: []string{"SWE RS12"}, time: -1},
		{q: "SWE RS12", lines: []string{"SWE RS12"}, time: -1},
		{q: "ICE 579", lines: []string{"ICE 579"}, trips: []string{"ICE 579 579 10:00"}, notLines: []string{"ICE 1579"}, time: -1},
		{q: "ice579", lines: []string{"ICE 579"}, trips: []string{"ICE 579 579 10:00"}, time: -1},
		{q: "IC 2010", lines: []string{"IC 2010"}, trips: []string{"IC 2010 2010 08:20"}, time: -1},
		{q: "ic2010", lines: []string{"IC 2010"}, time: -1},
		{q: "IC 2011", lines: []string{"IC 2011"}, time: -1},
		{q: "1918", lines: []string{"IC 1918"}, trips: []string{"IC 1918 1918 10:00"}, time: -1},
		{q: "ICE 1530", lines: []string{"ICE 1530"}, time: -1},
		{q: "579", lines: []string{"ICE 579"}, trips: []string{"ICE 579 579 10:00"}, time: -1},
		{q: "12345", trips: []string{"RB 12345 12345 06:40"}, time: -1},
		{q: "10123", trips: []string{"RE 7 10123 05:20"}, time: -1},
		{q: "7", lines: []string{"RE 7", "EC 7", "NX RE7"}, notLines: []string{"ICE 1067"}, time: -1},
		{q: "SEV", lines: []string{"Bus SEV"}, time: -1},
		{q: "ice", lines: []string{"ICE 57", "ICE 579"}, time: -1},

		// --- multiple tokens, time, direction
		{q: "horrem s12", las: []string{"S 12@Horrem"}, stations: []string{"Horrem"}, time: -1},
		{q: "s12 horrem", las: []string{"S 12@Horrem"}, time: -1},
		{q: "Horrem S 12", las: []string{"S 12@Horrem"}, stations: []string{"Horrem"}, maxSt: 1, time: -1},
		{q: "horrem s 12", las: []string{"S 12@Horrem"}, time: -1},
		{q: "horrem s12 14:30", las: []string{"S 12@Horrem"}, trips: []string{"S 12 38129 14:30@Horrem", "S 12 38130 14:50@Horrem"}, time: 870},
		{q: "horrem s12 1430", las: []string{"S 12@Horrem"}, trips: []string{"S 12 38129 14:30@Horrem"}, time: 870},
		{q: "horrem s12 um 14 uhr", trips: []string{"S 12 38128 14:10@Horrem"}, time: 840},
		{q: "köln re7", las: []string{"RE 7@Köln Hbf", "NX RE7@Köln Hbf"}, lines: []string{"RE 7", "NX RE7", "RE 70"}, time: -1},
		{q: "köln re 7", las: []string{"RE 7@Köln Hbf", "NX RE7@Köln Hbf"}, time: -1},
		{q: "frankfurt s8", las: []string{"S 8@Frankfurt (Main) Hbf"}, time: -1},
		{q: "köln ice", las: []string{"ICE 579@Köln Hbf", "ICE 1067@Köln Hbf"}, stations: []string{"Köln Hbf"}, time: -1},
		{q: "köln 18:30", stations: []string{"Köln Hbf"}, time: 1110},
		{q: "köln 1830", stations: []string{"Köln Hbf"}, time: 1110},
		{q: "koeln 7 uhr", stations: []string{"Köln Hbf"}, time: 420},
		{q: "aachen hbf 8 uhr", stations: []string{"Aachen Hbf"}, maxSt: 1, time: 480},
		{q: "porz 14.30", stations: []string{"Porz(Rhein)"}, time: 870},
		{q: "muenster 14 uhr 30", stations: []string{"Münster (Westf) Hbf"}, time: 870},
		{q: "18:30", stations: []string{}, time: 1110},
		{q: "nach Aachen", stations: []string{"Aachen Hbf", "Aachen West"}, notSt: []string{"Eisenach", "Nachrodt"}, time: -1},
		{q: "re7 nach krefeld", lines: []string{"RE 7", "NX RE7"}, las: []string{}, notLines: []string{"RE 70"}, time: -1},
		{q: "s12 richtung köln", lines: []string{"S 12"}, las: []string{}, time: -1},
		{q: "köln s12 14:30 nach troisdorf", trips: []string{"S 12 38129 14:40@Köln Hbf"}, time: 870},

		// --- lines at the station: most frequent first (not only IC/ICE)
		{q: "köln hbf", stLinesHd: []string{"S 12", "S 6", "S 11", "RE 1"}, time: -1},
		{q: "xyzzy", stations: []string{}, lines: []string{}, time: -1},
	}

	for _, c := range cases {
		res := runSearch(st, di, c.q, maxStations)
		var gotSt, gotLi, gotLas, gotTrips, gotSL []string
		for _, s := range res.Stations {
			gotSt = append(gotSt, s.Name)
		}
		for _, l := range res.Lines {
			gotLi = append(gotLi, l.Label)
		}
		for _, x := range res.LineAtStation {
			gotLas = append(gotLas, x.Line.Label+"@"+x.Station)
		}
		for _, x := range res.Trips {
			s := x.Label + " " + x.Number + " " + x.Dep
			if x.Station != "" {
				s += "@" + x.Station
			}
			gotTrips = append(gotTrips, s)
		}
		for _, l := range res.StationLines {
			gotSL = append(gotSL, l.Label)
		}
		fail := func(what string, got, want any) {
			t.Errorf("%q: %s = %v, want %v", c.q, what, got, want)
		}
		if !prefixOK(gotSt, c.stations) {
			fail("stations", gotSt, c.stations)
		}
		if !prefixOK(gotLi, c.lines) {
			fail("lines", gotLi, c.lines)
		}
		if !prefixOK(gotLas, c.las) {
			fail("lineAtStation", gotLas, c.las)
		}
		if !prefixOK(gotTrips, c.trips) {
			fail("trips", gotTrips, c.trips)
		}
		if !prefixOK(gotSL, c.stLinesHd) {
			fail("stationLines", gotSL, c.stLinesHd)
		}
		if c.maxSt > 0 && len(gotSt) > c.maxSt {
			fail("stations (too many)", gotSt, c.maxSt)
		}
		if c.stName != "" && res.StationName != c.stName {
			fail("stationName", res.StationName, c.stName)
		}
		for _, n := range c.notLines {
			for _, g := range gotLi {
				if g == n {
					fail("lines (unwanted "+n+")", gotLi, c.lines)
				}
			}
		}
		for _, n := range c.notSt {
			for _, g := range gotSt {
				if g == n {
					fail("stations (unwanted "+n+")", gotSt, c.stations)
				}
			}
		}
		switch {
		case c.time == -1 && res.Time != nil:
			fail("time", *res.Time, "none")
		case c.time >= 0 && (res.Time == nil || *res.Time != c.time):
			got := "none"
			if res.Time != nil {
				got = fmt.Sprint(*res.Time)
			}
			fail("time", got, c.time)
		}
		// lineAtStation only for lines that actually stop there:
		for _, x := range res.LineAtStation {
			l := di.byKey[x.Line.Key]
			if l == nil || l.Stations[st.list[st.byName[x.Station]].Eva] == 0 {
				t.Errorf("%q: lineAtStation %s@%s does not stop there", c.q, x.Line.Label, x.Station)
			}
		}
		if len(res.Stations) > maxStations || len(res.Lines) > maxLines || len(res.Trips) > maxTrips {
			t.Errorf("%q: limits exceeded", c.q)
		}
	}
}

func TestSearchDeterministic(t *testing.T) {
	st := fixtureStationIndex()
	for _, q := range []string{"7", "ice", "s 1", "köln", "re"} {
		first := fmt.Sprint(runSearch(st, fixtureDayIndex(st), q, maxStations))
		for i := 0; i < 20; i++ {
			if got := fmt.Sprint(runSearch(st, fixtureDayIndex(st), q, maxStations)); got != first {
				t.Fatalf("%q: nondeterministic result", q)
			}
		}
	}
}

func TestSearchWithoutDayIndex(t *testing.T) {
	// index not built yet: stations still work, no lines, no panic.
	st := fixtureStationIndex()
	res := runSearch(st, nil, "horrem s12 14:30", maxStations)
	if len(res.Stations) == 0 || res.Stations[0].Name != "Horrem" || len(res.Lines) != 0 || res.Time == nil {
		t.Errorf("unexpected: %+v", res)
	}
	res = runSearch(newStationIndex(nil, time.Time{}), nil, "köln", maxStations)
	if len(res.Stations) != 0 {
		t.Errorf("empty index must not match")
	}
}

func TestDayIndexBuild(t *testing.T) {
	st := fixtureStationIndex()
	di := fixtureDayIndex(st)
	if di.byKey["RE|99"] != nil {
		t.Error("a trip of another service day must be skipped")
	}
	if di.byKey["RE|RE7"] != nil {
		t.Error("tl-less fchg rows must not override the <tl> category (NX|RE7)")
	}
	// a run with only tl-less rows → assigned to the <tl> line with the same
	// label category+number.
	acc := newDayAcc(fixtureDay)
	mk := func(run, cat, label string, tl bool) {
		acc.add(idxRow{StopID: run + "-1", Eva: "1", Direction: "departure", Cat: cat, LineLabel: label, HasTL: tl, Planned: fixtureDay.Add(9 * time.Hour)})
		acc.add(idxRow{StopID: run + "-2", Eva: "2", Direction: "arrival", Cat: cat, LineLabel: label, HasTL: tl, Planned: fixtureDay.Add(10 * time.Hour)})
	}
	mk("1-2610300900", "SWE", "RS12", true)
	mk("2-2610300900", "RS", "RS12", false)
	mk("3-2610300900", "XY", "XY9", false)
	snap := acc.snapshot("d", fixtureDay, fixtureDay)
	if snap.byKey["RS|RS12"] != nil || len(snap.byKey["SWE|RS12"].Trips) != 2 || snap.byKey["XY|XY9"] == nil {
		t.Errorf("tl-less mapping: %v", snap.byKey)
	}
	l := di.byKey["S|12"]
	if l == nil || len(l.Trips) != 40 {
		t.Fatalf("S|12 trips: %v", l)
	}
	tr := l.Trips[0]
	if tr.Stops[0].Idx != 1 || tr.Stops[0].Min != 290 || len(tr.Stops) != 7 {
		t.Errorf("first S12 trip stops: %+v", tr.Stops)
	}
	if got := di.byTrainNo["10123"]; len(got) != 1 || got[0].Line.Key != "RE|7" {
		t.Errorf("train number index: %v", got)
	}
	// re-adding the same rows is idempotent (stop map keyed by index):
	acc = newDayAcc(fixtureDay)
	row := idxRow{StopID: "1-2610300800-1", Eva: "8000000", Direction: "departure", Cat: "S", LineLabel: "12",
		TrainNo: "38000", HasTL: true, Planned: fixtureDay.Add(8 * time.Hour)}
	acc.add(row)
	acc.add(row)
	row.StopID, row.Direction = "1-2610300800-2", "arrival"
	acc.add(row)
	if s := acc.snapshot("d", fixtureDay, fixtureDay); len(s.Trips["1-2610300800"].Stops) != 2 {
		t.Error("idempotent add failed")
	}
}

func TestParseQueryRoles(t *testing.T) {
	st := fixtureStationIndex()
	isSt := func(ws []string) bool { return len(st.match(ws, nil)) > 0 }
	cats := map[string]bool{}
	for _, c := range defaultCats {
		cats[c] = true
	}
	cases := []struct {
		q, want string
	}{
		{"RE 7", "cat=re num=7"},
		{"re7", "cat=re num=7"},
		{"S6", "cat=s num=6"},
		{"ice579", "cat=ice num=579"},
		{"IC 2010", "cat=ic num=2010"},     // category before → no time
		{"2010", "num=2010"},               // without a station → number
		{"köln 2010", "st=koln time=1210"}, // with a station → time
		{"köln ic 2010", "st=koln cat=ic num=2010"},
		{"porz 14:30", "st=porz time=870"},
		{"1430 uhr", "time=870"},
		{"um 7", "time=420"},
		{"14h30 köln", "st=koln time=870"},
		{"re7 nach krefeld", "cat=re num=7 dest=krefeld"},
		{"ab horrem s12 gleis 3", "st=horrem cat=s num=12"},
		{"ice", "st=ice cat=ice only"},
		{"nx re7", "cat=re num=7"},
		{"köln ice", "st=koln cat=ice only"},
		{"xyz12", "cat=xyz num=12"},
	}
	for _, c := range cases {
		pq := parseQuery(c.q, cats, isSt)
		var parts []string
		if len(pq.station) > 0 {
			parts = append(parts, "st="+strings.Join(pq.station, ","))
		}
		if pq.cat != "" {
			parts = append(parts, "cat="+pq.cat)
		}
		if pq.num != "" {
			parts = append(parts, "num="+pq.num)
		}
		if pq.catOnly {
			parts = append(parts, "only")
		}
		if len(pq.dest) > 0 {
			parts = append(parts, "dest="+strings.Join(pq.dest, ","))
		}
		if pq.hasTime {
			parts = append(parts, fmt.Sprintf("time=%d", pq.time))
		}
		if got := strings.Join(parts, " "); got != c.want {
			t.Errorf("parseQuery(%q) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestFoldWords(t *testing.T) {
	cases := map[string]string{
		"Köln Hbf":           "koln hbf",
		"S 6":                "s 6",
		"ice579":             "ice 579",
		"Frankfurt(Main)Hbf": "frankfurt main hbf",
		"Straße":             "strasse",
		"Köln Messe/Deutz":   "koln messe deutz",
	}
	for in, want := range cases {
		if got := strings.Join(foldWords(in, false), " "); got != want {
			t.Errorf("foldWords(%q) = %q, want %q", in, got, want)
		}
	}
	if got := strings.Join(foldWords("Münster", true), ""); got != "muenster" {
		t.Errorf("ae form: %q", got)
	}
}

func TestHHMM(t *testing.T) {
	for in, want := range map[int]string{0: "00:00", 870: "14:30", 1439: "23:59", 1440 + 65: "01:05", -10: "23:50"} {
		if got := hhmm(in); got != want {
			t.Errorf("hhmm(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestParseDay(t *testing.T) {
	days := []string{"2026-10-28", "2026-10-29", "2026-10-30"}
	if s, _, _, err := parseDay("", days); err != nil || s.Day() != 30 {
		t.Errorf("empty day must resolve to the latest day: %v %v", s, err)
	}
	if s, _, _, err := parseDay("2026-10-29", days); err != nil || s.Day() != 29 {
		t.Errorf("known day must parse: %v %v", s, err)
	}
	for _, bad := range []string{"2026-10-31", "2026-10-27", "30.10.2026", "x"} {
		if _, _, _, err := parseDay(bad, days); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	if _, _, _, err := parseDay("", nil); err == nil {
		t.Error("no prepared days must be an error")
	}
}

func TestBoundedCache(t *testing.T) {
	c := newBoundedCache[int](2)
	now := time.Now()
	c.put("a", 1, now)
	c.put("b", 2, now.Add(time.Second))
	c.get("a", now.Add(2*time.Second)) // a is now more recently used than b
	c.put("c", 3, now.Add(3*time.Second))
	if _, ok := c.get("b", now.Add(4*time.Second)); ok {
		t.Error("LRU entry b should have been evicted")
	}
	if _, ok := c.get("a", now.Add(4*time.Second)); !ok {
		t.Error("a must survive")
	}
	if _, ok := c.get("c", now.Add(4*time.Second)); !ok {
		t.Error("c must survive")
	}
	if c.len() > 2 {
		t.Error("cap exceeded")
	}
}
