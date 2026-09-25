# Lab 3 test API

The service stores orders in PostgreSQL and exposes:

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
