-- 002: store one row per stop visit instead of every raw prediction.
--
-- Why: the trip-updates feed lists a *predicted* arrival for every stop a bus
-- still has ahead of it, and repeats the whole list on every poll. Appending all
-- of that (what 001's stop_time_updates table did) stores tens of thousands of
-- rows per poll, and almost all of them are guesses that later turned out wrong.
--
-- Instead we keep exactly one row per (service day, trip, stop) and overwrite it
-- each poll. While the bus is approaching, the row holds the latest prediction.
-- Once the bus passes the stop, the feed stops mentioning it, the row stops
-- changing, and its final value is our best record of when the bus actually
-- arrived. Storage drops to roughly one row per scheduled stop per day.
--
-- stop_time_updates from 001 is no longer written to. It is left in place so no
-- previously collected data is deleted; drop it by hand once you don't need it.

-- Extra schedule columns the loader (cmd/loadschedule) fills in.
ALTER TABLE trips      ADD COLUMN IF NOT EXISTS service_id   TEXT;
ALTER TABLE trips      ADD COLUMN IF NOT EXISTS direction_id INT;
ALTER TABLE trips      ADD COLUMN IF NOT EXISTS headsign     TEXT;
-- Scheduled arrival as seconds since the start of the service day. Easier to do
-- math on than the "HH:MM:SS" text in arrival_time (which can exceed 24:00:00).
ALTER TABLE stop_times ADD COLUMN IF NOT EXISTS arrival_secs INT;

CREATE TABLE IF NOT EXISTS stop_visits (
    service_date      DATE        NOT NULL,  -- the service day the trip belongs to
    trip_id           TEXT        NOT NULL,
    stop_sequence     INT         NOT NULL,  -- position of the stop within the trip
    route_id          TEXT,                  -- from the feed; may be NULL (see view)
    stop_id           TEXT        NOT NULL,
    feed_delay_secs   INT,                   -- lateness reported by the feed, if any
    predicted_arrival TIMESTAMPTZ,           -- predicted arrival time, if reported
    first_seen_at     TIMESTAMPTZ NOT NULL,  -- first snapshot that mentioned this stop
    last_seen_at      TIMESTAMPTZ NOT NULL,  -- latest snapshot that mentioned it
    observations      INT         NOT NULL DEFAULT 1,
    PRIMARY KEY (service_date, trip_id, stop_sequence)
);

-- Every dashboard query filters by route and a time range.
CREATE INDEX IF NOT EXISTS idx_stop_visits_route_seen ON stop_visits (route_id, last_seen_at);
CREATE INDEX IF NOT EXISTS idx_stop_visits_seen       ON stop_visits (last_seen_at);

-- visit_delays works out how late each visit was, so queries don't repeat the math.
--
-- Some feeds report lateness directly (feed_delay_secs). Others only give a
-- predicted arrival time, and lateness = predicted - scheduled. The scheduled
-- time comes from stop_times, using the GTFS rule that schedule times count
-- from "noon minus 12 hours" on the service day. That equals midnight on every
-- day except the two daylight-saving switch days, where using real midnight
-- would make every bus look an hour early or late.
CREATE OR REPLACE VIEW visit_delays AS
SELECT
    s.service_date,
    s.trip_id,
    s.stop_sequence,
    s.route_id,
    s.stop_id,
    s.feed_delay_secs,
    s.predicted_arrival,
    s.scheduled_arrival,
    s.first_seen_at,
    s.last_seen_at,
    s.observations,
    COALESCE(
        s.feed_delay_secs,
        EXTRACT(EPOCH FROM (s.predicted_arrival - s.scheduled_arrival))::int
    ) AS delay_secs
FROM (
    SELECT
        v.service_date,
        v.trip_id,
        v.stop_sequence,
        -- Fall back to the schedule's route if the feed left route_id out.
        COALESCE(v.route_id, t.route_id) AS route_id,
        v.stop_id,
        v.feed_delay_secs,
        v.predicted_arrival,
        ((v.service_date + TIME '12:00') AT TIME ZONE 'America/Edmonton')
            - INTERVAL '12 hours'
            + st.arrival_secs * INTERVAL '1 second' AS scheduled_arrival,
        v.first_seen_at,
        v.last_seen_at,
        v.observations
    FROM stop_visits v
    LEFT JOIN trips t
           ON t.trip_id = v.trip_id
    LEFT JOIN stop_times st
           ON st.trip_id = v.trip_id
          AND st.stop_sequence = v.stop_sequence
) s;
