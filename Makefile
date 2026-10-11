# Shortcuts for the common commands. Run `make <target>`, e.g. `make db`.
#
# Settings are read from .env if it exists (copy .env.example to start).
# `-include` loads it without failing if it's missing; `export` passes every
# variable on to the commands below.
-include .env
export

TEST_DATABASE_URL ?= postgres://ctt:ctt@localhost:5432/ctt_test

.PHONY: db db-stop schedule collect api poc test test-db fmt

db:            ## start PostgreSQL in Docker
	docker compose up -d --wait

db-stop:       ## stop PostgreSQL (data is kept)
	docker compose down

schedule:      ## download and load the static schedule
	go run ./cmd/loadschedule

collect:       ## run the feed collector (Ctrl+C to stop)
	go run ./cmd/collector

api:           ## run the API and dashboard on http://localhost:8080
	go run ./cmd/api

poc:           ## print a sample of the live feed
	go run ./cmd/poc

test:          ## unit tests (no database needed)
	go test ./...

test-db: db    ## unit + database tests, using a separate ctt_test database
	docker compose exec -T db psql -U ctt -d ctt -tc "SELECT 1 FROM pg_database WHERE datname = 'ctt_test'" | grep -q 1 \
		|| docker compose exec -T db createdb -U ctt ctt_test
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -count=1 ./...

fmt:           ## format all Go code
	gofmt -w .
