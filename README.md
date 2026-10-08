# Calgary Transit on-time tracker

Ingests Calgary Transit's live GTFS-Realtime feeds, compares reported positions
against the published schedule, and reports on-time performance by route, stop
and hour of day.

Transit apps show where a bus is right now. This answers a different question:
**how reliably does a given route run at a given time of day?**

## Architecture

```
  Calgary Transit          collector            PostgreSQL           API + dashboard
  GTFS-Realtime feed  ->   (Go service,     ->  stop_time_updates ->  on-time % by
  (trip updates)           polls every 45s)     (raw readings)        route and hour
```

- **collector** (`cmd/collector`) polls the trip-updates feed every 45s and
  stores each stop-level delay reading.
- **PostgreSQL** holds the raw readings plus the static schedule.
- **API + dashboard** aggregate the readings into on-time percentages.

GTFS-Realtime is a Protocol Buffers (binary) format. `internal/gtfs` decodes it
using the official MobilityData bindings.

## Requirements

- [Go](https://go.dev/dl/) 1.24+
- [Docker](https://www.docker.com/products/docker-desktop/) (for PostgreSQL)

## Quick start

```bash
# Install dependencies
go mod tidy

# Fetch and print current delay readings (no database required)
go run ./cmd/poc

# Start PostgreSQL (runs migrations/001_init.sql on first boot)
docker compose up -d

# Start the collector
cp .env.example .env
go run ./cmd/collector
```

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/poc` | One-shot fetch-and-print of live delays |
| `cmd/collector` | Long-running feed poller that writes to the database |
| `cmd/api` | REST API serving aggregated on-time data |
| `internal/gtfs` | GTFS-Realtime feed fetching and decoding |
| `internal/store` | PostgreSQL access |
| `migrations` | Database schema |
| `web` | Dashboard front end |

## Configuration

Copy `.env.example` to `.env`. Secrets are read from the environment and are
never committed.

| Variable | Description |
| --- | --- |
| `DATABASE_URL` | PostgreSQL connection string |
| `GTFS_RT_TRIP_UPDATES` | Trip-updates feed URL |
| `GTFS_RT_VEHICLE_POSITIONS` | Vehicle-positions feed URL |

## Data & licence

Data from the [City of Calgary open data portal](https://data.calgary.ca/Transportation-Transit/Calgary-Transit-Realtime-Trip-Updates-GTFS-RT/gs4m-mdc2/data).

> Contains information licensed under the Open Government Licence – City of Calgary.
