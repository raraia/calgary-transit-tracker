package schedule

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestParseTime(t *testing.T) {
	good := map[string]int{
		"00:00:00": 0,
		"08:05:30": 8*3600 + 5*60 + 30,
		"25:10:00": 25*3600 + 10*60, // after midnight, previous day's timetable
		" 7:00:00": 7 * 3600,        // some feeds don't zero-pad hours
	}
	for in, want := range good {
		got, err := ParseTime(in)
		if err != nil || got != want {
			t.Errorf("ParseTime(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "8:05", "08:60:00", "aa:bb:cc", "-1:00:00"} {
		if _, err := ParseTime(bad); err == nil {
			t.Errorf("ParseTime(%q) should fail", bad)
		}
	}
}

// makeZip builds a GTFS zip in memory from file name -> contents.
func makeZip(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

// collect drains a table into a slice so tests can inspect it.
func collect(t *testing.T, rows *Rows) [][]any {
	t.Helper()
	var out [][]any
	for rows.Next() {
		v, _ := rows.Values()
		out = append(out, append([]any(nil), v...))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTables(t *testing.T) {
	feed := NewFeed(makeZip(t, map[string]string{
		// Leading byte order mark and columns in an unusual order: both must work.
		"routes.txt": "\ufeffroute_long_name,route_id,route_short_name\nCentre St,3,3\nBrentwood,201,201\n",
		"stops.txt":  "stop_id,stop_name,stop_lat,stop_lon\n1001,City Hall,51.0453,-114.0581\n1002,No Coordinates,,\n",
		"trips.txt":  "route_id,service_id,trip_id,trip_headsign,direction_id\n201,WKDY,t1,Somerset,0\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"t1,08:00:00,08:00:30,1001,1\n" +
			"t1,,08:10:00,1002,2\n" + // blank arrival -> departure used
			"t1,,,1003,3\n" + // no times at all -> NULLs
			"t1,24:30:00,24:30:00,1004,4\n", // after midnight
	}))
	defer feed.Close()

	tables, err := feed.Tables()
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 4 || tables[0].Name != "routes" || tables[3].Name != "stop_times" {
		t.Fatalf("unexpected tables: %+v", tables)
	}

	routes := collect(t, tables[0].Rows)
	if len(routes) != 2 || routes[0][0] != "3" || routes[0][1] != "3" || routes[0][2] != "Centre St" {
		t.Errorf("routes = %v", routes)
	}

	stops := collect(t, tables[1].Rows)
	if stops[0][2] != 51.0453 || stops[1][2] != nil {
		t.Errorf("stops = %v (missing coordinates should become nil)", stops)
	}

	trips := collect(t, tables[2].Rows)
	if trips[0][0] != "t1" || trips[0][3] != 0 || trips[0][4] != "Somerset" {
		t.Errorf("trips = %v", trips)
	}

	st := collect(t, tables[3].Rows)
	if st[0][4] != 8*3600 {
		t.Errorf("first stop arrival_secs = %v", st[0][4])
	}
	if st[1][3] != "08:10:00" || st[1][4] != 8*3600+10*60 {
		t.Errorf("departure fallback = %v", st[1])
	}
	if st[2][3] != nil || st[2][4] != nil {
		t.Errorf("missing times should be nil, got %v", st[2])
	}
	if st[3][4] != 24*3600+30*60 {
		t.Errorf("after-midnight time = %v", st[3][4])
	}
}

func TestTablesMissingFile(t *testing.T) {
	feed := NewFeed(makeZip(t, map[string]string{"routes.txt": "route_id\n1\n"}))
	defer feed.Close()
	if _, err := feed.Tables(); err == nil {
		t.Error("expected an error when stops.txt is missing")
	}
}

func TestBadRowReportsLine(t *testing.T) {
	feed := NewFeed(makeZip(t, map[string]string{
		"routes.txt":     "route_id\n1\n",
		"stops.txt":      "stop_id,stop_lat,stop_lon\n1,not-a-number,0\n",
		"trips.txt":      "trip_id,route_id\n",
		"stop_times.txt": "trip_id,stop_id,stop_sequence\n",
	}))
	defer feed.Close()
	tables, err := feed.Tables()
	if err != nil {
		t.Fatal(err)
	}
	rows := tables[1].Rows
	for rows.Next() {
	}
	if rows.Err() == nil {
		t.Fatal("expected a parse error")
	}
	if got := rows.Err().Error(); !bytes.Contains([]byte(got), []byte("stops.txt line 2")) {
		t.Errorf("error should name the file and line, got %q", got)
	}
}
