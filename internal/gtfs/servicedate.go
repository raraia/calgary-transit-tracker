package gtfs

import "time"

// serviceDayCutoff is how far after midnight late-night trips still belong to
// the previous day's service. A bus scheduled for 25:10 ("1:10 am") is part of
// yesterday's timetable, not today's.
const serviceDayCutoff = 4 * time.Hour

// ServiceDate returns the service day a trip belongs to, as midnight UTC on
// that calendar date (only the year, month and day matter).
//
// The feed's own start_date ("YYYYMMDD") is used when present because it is
// authoritative. If it's missing or malformed, we guess from when the snapshot
// was observed, in local time: anything before 4 am counts as the previous day.
func ServiceDate(startDate string, observed time.Time, loc *time.Location) time.Time {
	if d, err := time.Parse("20060102", startDate); err == nil {
		return d // time.Parse without a zone already returns UTC midnight
	}
	local := observed.In(loc).Add(-serviceDayCutoff)
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
