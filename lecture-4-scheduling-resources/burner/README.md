# Lab 4 burner

The burner is disposable background load for the scheduling and resource
pressure experiments in Lab 4. It continuously consumes CPU and keeps a
configurable amount of memory in its active working set by rewriting every
memory page once per second. It has no business function.

The lab calls the Kubernetes workload `batch`; `burner` is the image and
binary used by that workload. The chart value controlling synthetic memory
usage should be passed to the container as `MEMORY_MIB`.

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
