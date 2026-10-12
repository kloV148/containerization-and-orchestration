# Labs 3 and 6 test API

The service stores orders in PostgreSQL and exposes:

- `GET /version` — returns plain text `v1` or `v2`, selected by `VERSION` (default: `v1`);
- `GET /health` — returns `200 ok`, or `503` when `HEALTH_FAIL=true`;
- `POST /order` — creates an order from `{"item":"keyboard","quantity":2}`;
- `GET /orders` — returns all orders, including their processing status;
- `GET /metrics` — exposes Prometheus metrics.

Set `DATABASE_URL` to a PostgreSQL connection string. Alternatively, use `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`, and `PGSSLMODE`. The defaults are suitable for a CloudNativePG service named `postgres-rw`: database and user `app`, port `5432`, and disabled TLS.

Run locally:

```shell
DATABASE_URL='postgres://app:password@localhost:5432/app?sslmode=disable' go run .
```

Create and list an order:

```shell
curl -i -X POST http://localhost:8080/order \
  -H 'Content-Type: application/json' \
  -d '{"item":"keyboard","quantity":2}'
curl http://localhost:8080/orders
```

## Lab 6 release modes

The same image runs both versions; set environment variables when starting it:

| VERSION | Switch | POST /order behavior |
| --- | --- | --- |
| v1 | none | Creates an order normally |
| v1 | FLAKY=true | Independently returns HTTP 500 for about 20% of requests |
| v2 | FAIL=false | Creates an order normally |
| v2 | FAIL=true | Always returns HTTP 500 |

`FAIL` only affects v2; `FLAKY` only affects v1. Both default to false.
An unsupported `VERSION` causes startup to fail. Failure injection happens before
a database write. `/health`, `/version`, and `/orders` remain available in these
release modes; `HEALTH_FAIL` independently controls the health endpoint.
All injected 500 responses are included in the existing request, error, and
latency metrics at `/metrics`. The application does not retry requests itself.

```shell
VERSION=v1 FLAKY=true DATABASE_URL='postgres://app:password@localhost:5432/app?sslmode=disable' go run .
VERSION=v2 FAIL=true DATABASE_URL='postgres://app:password@localhost:5432/app?sslmode=disable' go run .
curl http://localhost:8080/version
```

The gate script and mesh traffic-split configuration are separate components.
