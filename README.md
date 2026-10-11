# Calgary Transit on-time tracker

[![CI](https://github.com/raraia/calgary-transit-tracker/actions/workflows/ci.yml/badge.svg)](https://github.com/raraia/calgary-transit-tracker/actions/workflows/ci.yml)

Ingests Calgary Transit's live GTFS-Realtime feed, compares arrivals against the
published schedule, and reports on-time performance by route, stop and hour of
day through a JSON API and a small dashboard.

Transit apps show where a bus is right now. This answers a different question:
**how reliably does a given route run at a given time of day?**

## Architecture

```
  GTFS-Realtime feed        collector             PostgreSQL               api
  (trip updates, every  ->  polls, decodes,  ->  stop_visits         ->  /api/routes ...
   ~30s, protobuf)          upserts visits        (one row per visit)     + dashboard at /
                                                        ^
  GTFS static schedule  ->  loadschedule  ------------->|  routes, stops, trips, stop_times
  (zip of CSVs)             (bulk COPY)
```

| Component | Purpose |
| --- | --- |
| `cmd/collector` | Polls the trip-updates feed and records stop visits |
| `cmd/loadschedule` | Downloads the static GTFS schedule and bulk-loads it |
| `cmd/api` | Serves the JSON API and the embedded dashboard |
| `cmd/poc` | One-shot fetch that prints a sample of the live feed |

### How delay is measured

The trip-updates feed publishes *predicted* arrivals for every stop a bus still
has ahead of it, and repeats the full list on every poll. Storing each prediction
would grow by tens of thousands of rows per poll and would measure forecasts
rather than outcomes.

Instead, the collector keeps **one row per stop visit** (service date, trip, stop
sequence) and overwrites it each poll. Once the bus passes the stop, the feed
stops mentioning it and the row's final value is the best available record of
the actual arrival. Only visits that have stopped updating ("settled") are
counted.

Lateness is taken from the feed's `delay` field when present; otherwise it is
computed as predicted arrival minus scheduled arrival, using the GTFS
"noon minus 12 hours" service-day rule so results stay correct across
daylight-saving changes. An arrival counts as **on time** if it is no more than
1 minute early and no more than 5 minutes late.

## Requirements

- [Go](https://go.dev/dl/) 1.24+
- [Docker](https://www.docker.com/products/docker-desktop/) (for PostgreSQL)

## Quick start

```bash
cp .env.example .env     # optional: defaults work for local development
make db                  # start PostgreSQL
make schedule            # load the static schedule (route/stop names, timetable)
make collect             # start collecting; leave this running
make api                 # in another terminal; open http://localhost:8080
```

The database schema is created and upgraded automatically on startup. Without
`make`, run the same commands directly, e.g. `go run ./cmd/collector`.

## API

All endpoints are `GET` and return JSON. `days` defaults to 7 (range 1–90).

| Endpoint | Description |
| --- | --- |
| `/healthz` | Service and database health |
| `/api/routes?days=7` | All routes with on-time share and average delay, least reliable first |
| `/api/routes/{id}/hourly?days=7` | On-time share by hour of day (local time). Add `format=csv` for CSV |
| `/api/routes/{id}/stops?days=7&limit=10` | Stops with the highest average delay (limit 1–50) |

Example response from `/api/routes/201/hourly`:

```json
{
  "route_id": "201",
  "days": 7,
  "window": { "max_early_secs": 60, "max_late_secs": 300 },
  "hours": [
    { "hour": 7, "visits": 412, "on_time_ratio": 0.71, "avg_delay_secs": 188.4 }
  ]
}
```

Requests are rate-limited per client IP (HTTP 429 when exceeded), inputs are
validated, all queries are parameterised, and responses carry a strict
Content-Security-Policy.

## Configuration

Settings are read from environment variables (`make` loads `.env` automatically).

| Variable | Default | Description |
| --- | --- | --- |
| `DATABASE_URL` | `postgres://ctt:ctt@localhost:5432/ctt` | PostgreSQL connection string |
| `GTFS_RT_TRIP_UPDATES` | City of Calgary feed | Trip-updates feed URL |
| `GTFS_STATIC_URL` | City of Calgary feed | Static schedule zip URL |
| `POLL_INTERVAL` | `30s` | Collector poll interval (minimum `10s`) |
| `API_ADDR` | `:8080` | API listen address |

## Development

```bash
make test       # unit tests, no database required
make test-db    # unit tests plus database tests against a separate ctt_test database
```

Database tests in `internal/store` run only when `TEST_DATABASE_URL` is set, and
they reset that database, so never point it at real data. CI runs the full suite
against PostgreSQL 16, along with `gofmt`, `go vet` and `govulncheck`.

### Layout

| Path | Contents |
| --- | --- |
| `internal/gtfs` | Feed fetching, protobuf decoding, service-date rules |
| `internal/schedule` | Streaming GTFS schedule parser |
| `internal/collector` | Poll loop: snapshot to stop visits |
| `internal/store` | PostgreSQL access, migrations, statistics queries |
| `internal/ontime` | The on-time window definition |
| `internal/api` | HTTP handlers and middleware |
| `migrations` | SQL schema, embedded into the binaries |
| `web` | Dashboard (HTML/CSS/JS, embedded into the API binary) |

## Data & licence

Data from the [City of Calgary open data portal](https://data.calgary.ca/Transportation-Transit/Calgary-Transit-Realtime-Trip-Updates-GTFS-RT/gs4m-mdc2/data).

> Contains information licensed under the Open Government Licence – City of Calgary.
