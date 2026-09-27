# Lab 3 test worker

The worker polls PostgreSQL for unprocessed orders and marks them processed.
Multiple replicas coordinate through `FOR UPDATE SKIP LOCKED`, so each order is
claimed by at most one worker.

It exposes:

- `GET /health` — returns `200 ok`, or `503` when `HEALTH_FAIL=true`;
- `GET /metrics` — exposes processed-order and database-error counters.

Set `DATABASE_URL` to a PostgreSQL connection string. Alternatively, use
`PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`, and `PGSSLMODE`.
`POLL_INTERVAL` controls how often an empty queue is checked and defaults to
`1s`.

Run locally:

```shell
DATABASE_URL='postgres://app:password@localhost:5432/app?sslmode=disable' go run .
```
