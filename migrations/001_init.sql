-- Schema for the transit tracker. This file runs automatically the first time
-- the Postgres container starts (see docker-compose.yml).

-- ---- Static schedule (loaded once from the GTFS schedule zip) --------------

CREATE TABLE IF NOT EXISTS routes (
    route_id    TEXT PRIMARY KEY,
    short_name  TEXT,          -- e.g. "1", "301" — what riders call the route
    long_name   TEXT
);

CREATE TABLE IF NOT EXISTS stops (
    stop_id   TEXT PRIMARY KEY,
    stop_name TEXT,
    lat       DOUBLE PRECISION,
    lon       DOUBLE PRECISION
);

CREATE TABLE IF NOT EXISTS trips (
    trip_id  TEXT PRIMARY KEY,
    route_id TEXT REFERENCES routes(route_id)
);

-- The scheduled time a trip is supposed to reach each stop.
CREATE TABLE IF NOT EXISTS stop_times (
    trip_id        TEXT NOT NULL,
    stop_id        TEXT NOT NULL,
    stop_sequence  INT  NOT NULL,   -- order of this stop within the trip
    arrival_time   TEXT,            -- "HH:MM:SS"; can exceed 24:00 for after-midnight trips
    PRIMARY KEY (trip_id, stop_sequence)
);

-- ---- Live observations (written by the collector every 30-60s) -------------

-- Raw delay readings: at time T, trip X was N seconds late reaching stop S.
-- This is the table that grows; everything in the dashboard is aggregated from it.
CREATE TABLE IF NOT EXISTS stop_time_updates (
    id             BIGSERIAL PRIMARY KEY,
    observed_at    TIMESTAMPTZ NOT NULL,   -- when we fetched this reading
    trip_id        TEXT NOT NULL,
    route_id       TEXT,                   -- denormalized so the dashboard query is simple
    stop_id        TEXT NOT NULL,
    stop_sequence  INT,
    delay_seconds  INT                     -- + = late, - = early, from the feed
);

-- Indexes that make the dashboard's "by route, by hour" query fast once the
-- table has millions of rows.
CREATE INDEX IF NOT EXISTS idx_stu_route_time ON stop_time_updates (route_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_stu_stop_time  ON stop_time_updates (stop_id, observed_at);
