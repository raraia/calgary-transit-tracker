# Calgary Transit on-time tracker

Records where every Calgary bus is using the City's free real-time feeds,
compares that to the schedule, and shows which routes run late, by hour of day.

Transit apps tell you where your bus is *right now*. This answers the question a
commuter actually has: **how late does my route usually run at 8am?**

## Why this project

It's one build that shows the skills Calgary employers ask students for:

- a **data pipeline** that ingests a live feed (energy/finance employers care)
- a **backend** with a database and REST API (software roles)
- **Docker + CI/CD** and a cloud deploy (nearly every posting)
- a **local story** every Calgary interviewer gets instantly

And the data is free, public, and needs no permission to use.

## How it works

```
  Calgary Transit          collector            PostgreSQL           API + dashboard
  GTFS-Realtime feed  ->   (Go service,     ->  stop_time_updates ->  (on-time % by
  (trip updates)           polls every 45s)     table grows            route and hour)
```

1. **Collector** (`cmd/collector`) fetches the trip-updates feed every 45s and
   stores each "trip X is N seconds late at stop S" reading.
2. **Postgres** holds the raw readings plus the static schedule.
3. **API + dashboard** (built in weeks 3-4) aggregate the readings into on-time
   percentages by route, stop and hour.

GTFS-Realtime is a Google standard. The feed is binary (Protocol Buffers), not
JSON — `internal/gtfs` decodes it with the official bindings library.

## Getting started

You need [Go](https://go.dev/dl/) and [Docker Desktop](https://www.docker.com/products/docker-desktop/).

```bash
# 1. Grab the Go dependencies
go mod tidy

# 2. Week-1 win: see live bus delays right now, no database needed
go run ./cmd/poc

# 3. Start the database (runs migrations/001_init.sql automatically on first boot)
docker compose up -d

# 4. Start collecting. Leave this running; the table fills up over time.
cp .env.example .env
go run ./cmd/collector
```

## The four-week plan

- **Week 1 — collect.** Load the static schedule into Postgres; get the collector
  running on a small cloud server so data accumulates while you build the rest.
- **Week 2 — compute delays.** Match readings to scheduled stop times; aggregate
  minutes-late by route, stop and hour. Most of the tests go here.
- **Week 3 — API + dashboard.** Pick a route, see on-time % by hour, worst stops,
  today's alerts, plus a CSV export.
- **Week 4 — ship.** CI, deploy with HTTPS and a domain, README with an
  architecture diagram and a 2-minute demo video. Post one finding on LinkedIn.

## Security notes (talking points for later)

- The API uses rate limiting and validates all query parameters.
- Secrets live in `.env` (gitignored), never in the repo.
- CI runs dependency scanning (`govulncheck`) on every push.

## Data & licence

Vehicle positions, trip updates and the schedule come from the
[City of Calgary open data portal](https://data.calgary.ca/Transportation-Transit/Calgary-Transit-Realtime-Trip-Updates-GTFS-RT/gs4m-mdc2/data).

> Contains information licensed under the Open Government Licence – City of Calgary.
