// Package schedule reads a static GTFS schedule (the published timetable).
//
// A GTFS schedule is a .zip of CSV files: routes.txt, stops.txt, trips.txt,
// stop_times.txt and others. This package streams the four we need straight out
// of the zip, one row at a time, converting each into the columns our database
// tables expect. Nothing is fully loaded into memory, which matters because
// stop_times.txt has around a million rows for a city Calgary's size.
package schedule

import (
	"archive/zip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Table is one schedule table ready to be bulk-loaded.
//
// Rows has Next/Values/Err methods, which is exactly what pgx's CopyFrom wants
// (the pgx.CopyFromSource interface). Go interfaces are satisfied implicitly:
// *Rows never mentions pgx, it just happens to have the right methods.
type Table struct {
	Name    string
	Columns []string
	Rows    *Rows
}

// Feed is an opened GTFS zip file.
type Feed struct {
	zr     *zip.Reader
	closer io.Closer // the underlying file, if we opened one
	open   []io.Closer
}

// Open opens a GTFS zip file from disk.
func Open(path string) (*Feed, error) {
	rc, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	return &Feed{zr: &rc.Reader, closer: rc}, nil
}

// NewFeed wraps an already-open zip archive (handy for tests, which build a zip in memory).
func NewFeed(zr *zip.Reader) *Feed { return &Feed{zr: zr} }

// Close releases every open CSV file and the zip itself.
func (f *Feed) Close() error {
	for _, c := range f.open {
		c.Close()
	}
	if f.closer != nil {
		return f.closer.Close()
	}
	return nil
}

// Tables returns the four schedule tables, ordered so that referenced tables
// come first (routes before trips), which the database's foreign keys require.
func (f *Feed) Tables() ([]Table, error) {
	var tables []Table
	for _, spec := range specs {
		rows, err := f.rows(spec)
		if err != nil {
			return nil, err
		}
		tables = append(tables, Table{Name: spec.table, Columns: spec.columns, Rows: rows})
	}
	return tables, nil
}

// fileSpec says how to turn one GTFS file into rows of one database table.
type fileSpec struct {
	file    string   // file inside the zip
	table   string   // destination table
	columns []string // destination columns, in the order convert returns values
	convert func(r record) ([]any, error)
}

// specs maps the GTFS files we use onto our tables. Column names on the left of
// each r.get(...) are GTFS's names; the table columns are ours.
var specs = []fileSpec{
	{
		file: "routes.txt", table: "routes",
		columns: []string{"route_id", "short_name", "long_name"},
		convert: func(r record) ([]any, error) {
			return []any{r.get("route_id"), r.get("route_short_name"), r.get("route_long_name")}, nil
		},
	},
	{
		file: "stops.txt", table: "stops",
		columns: []string{"stop_id", "stop_name", "lat", "lon"},
		convert: func(r record) ([]any, error) {
			lat, err := optionalFloat(r.get("stop_lat"))
			if err != nil {
				return nil, err
			}
			lon, err := optionalFloat(r.get("stop_lon"))
			if err != nil {
				return nil, err
			}
			return []any{r.get("stop_id"), r.get("stop_name"), lat, lon}, nil
		},
	},
	{
		file: "trips.txt", table: "trips",
		columns: []string{"trip_id", "route_id", "service_id", "direction_id", "headsign"},
		convert: func(r record) ([]any, error) {
			dir, err := optionalInt(r.get("direction_id"))
			if err != nil {
				return nil, err
			}
			return []any{r.get("trip_id"), r.get("route_id"), r.get("service_id"), dir, r.get("trip_headsign")}, nil
		},
	},
	{
		file: "stop_times.txt", table: "stop_times",
		columns: []string{"trip_id", "stop_id", "stop_sequence", "arrival_time", "arrival_secs"},
		convert: func(r record) ([]any, error) {
			seq, err := strconv.Atoi(r.get("stop_sequence"))
			if err != nil {
				return nil, fmt.Errorf("stop_sequence: %w", err)
			}
			// Arrival is optional for stops between timing points; departure
			// is the fallback, and if both are blank we store NULL.
			raw := r.get("arrival_time")
			if raw == "" {
				raw = r.get("departure_time")
			}
			var arrivalText, arrivalSecs any // nil = SQL NULL
			if raw != "" {
				secs, err := ParseTime(raw)
				if err != nil {
					return nil, err
				}
				arrivalText, arrivalSecs = raw, secs
			}
			return []any{r.get("trip_id"), r.get("stop_id"), seq, arrivalText, arrivalSecs}, nil
		},
	},
}

// ParseTime converts a GTFS time like "08:05:30" into seconds since the start of
// the service day.
//
// GTFS times can go past 24:00:00: a trip that runs after midnight but belongs
// to the previous day's timetable is written as "25:10:00". That's why these are
// stored as a number of seconds, not as a clock time.
func ParseTime(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("bad GTFS time %q: want HH:MM:SS", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("bad GTFS time %q", s)
		}
		n[i] = v
	}
	if n[1] > 59 || n[2] > 59 {
		return 0, fmt.Errorf("bad GTFS time %q: minutes/seconds over 59", s)
	}
	return n[0]*3600 + n[1]*60 + n[2], nil
}

// --- streaming CSV rows -----------------------------------------------------

// Rows streams one GTFS file as database rows. It implements pgx.CopyFromSource.
type Rows struct {
	spec   fileSpec
	reader *csv.Reader
	header map[string]int
	values []any
	line   int
	err    error
}

func (f *Feed) rows(spec fileSpec) (*Rows, error) {
	zf, err := f.zr.Open(spec.file)
	if err != nil {
		return nil, fmt.Errorf("GTFS zip is missing %s: %w", spec.file, err)
	}
	f.open = append(f.open, zf)

	cr := csv.NewReader(zf)
	cr.ReuseRecord = true   // reuse one slice per row instead of allocating a million
	cr.FieldsPerRecord = -1 // tolerate rows with a missing trailing column

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: reading header: %w", spec.file, err)
	}
	idx := make(map[string]int, len(header))
	for i, name := range header {
		// Files saved by Excel often start with an invisible "byte order mark"
		// (U+FEFF). Left in, the first column would be named "\ufeffroute_id"
		// and every lookup of "route_id" would silently come back empty.
		name = strings.TrimPrefix(name, "\ufeff")
		idx[strings.TrimSpace(name)] = i
	}
	return &Rows{spec: spec, reader: cr, header: idx, line: 1}, nil
}

// Next advances to the next row, returning false at the end or on error.
func (r *Rows) Next() bool {
	if r.err != nil {
		return false
	}
	rec, err := r.reader.Read()
	if errors.Is(err, io.EOF) {
		return false
	}
	r.line++
	if err != nil {
		r.err = fmt.Errorf("%s line %d: %w", r.spec.file, r.line, err)
		return false
	}
	r.values, err = r.spec.convert(record{fields: rec, header: r.header})
	if err != nil {
		r.err = fmt.Errorf("%s line %d: %w", r.spec.file, r.line, err)
		return false
	}
	return true
}

// Values returns the current row's converted values.
func (r *Rows) Values() ([]any, error) { return r.values, nil }

// Err reports the error that stopped iteration, if any.
func (r *Rows) Err() error { return r.err }

// record gives column access by name, because GTFS files may order their
// columns however they like.
type record struct {
	fields []string
	header map[string]int
}

func (r record) get(name string) string {
	i, ok := r.header[name]
	if !ok || i >= len(r.fields) {
		return ""
	}
	return strings.TrimSpace(r.fields[i])
}

// optionalInt and optionalFloat return nil (SQL NULL) for empty strings.
func optionalInt(s string) (any, error) {
	if s == "" {
		return nil, nil
	}
	return strconv.Atoi(s)
}

func optionalFloat(s string) (any, error) {
	if s == "" {
		return nil, nil
	}
	return strconv.ParseFloat(s, 64)
}
