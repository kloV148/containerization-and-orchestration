# Lab 4 burner

The burner is disposable background load for the scheduling and resource
pressure experiments in Lab 4. It retains a configurable amount of resident
memory and continuously consumes CPU.

Configuration:

- `MEMORY_MIB` — resident memory to allocate, defaults to `64`;
- `CPU_WORKERS` — number of CPU-burning goroutines, defaults to `1`;
- `PORT` — health and metrics HTTP port, defaults to `8080`.

Endpoints:

- `GET /health` — returns `200 ok`;
- `GET /metrics` — exposes retained memory and CPU iteration metrics in the
  Prometheus text format.

Run locally:

```shell
MEMORY_MIB=128 CPU_WORKERS=2 go run .
```

Keep `MEMORY_MIB` below the container memory limit when testing node-pressure
eviction. If the process crosses its own cgroup limit, the result is
`OOMKilled`, which is a different experiment.
