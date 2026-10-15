# Canary gate

Requires Bash, curl, jq, kubectl and envsubst (gettext). Run one gate instance.
All settings are at the top of `gate.sh`, with environment overrides.

Start with a VirtualService split of v1=90 / v2=10, both subsets deployed,
and requests flowing through the waypoint. The gate reads the existing split.
It polls v2's backend 5xx fraction every 15 seconds over a one-minute
window. At least 20 v2 requests are required before making a decision.
An error fraction above 5% triggers rollback to 0%. Latency is not used.
Four consecutive healthy checks increase v2 by 15 percentage points.
Checks have overlapping windows; they are not independent observations.
After each promotion it waits 60 seconds for metrics to settle. Missing data
or Prometheus errors reset the healthy streak and leave weights unchanged.
The gate stops on rollback or promotion to 100%; restore 90/10 before a new run.

The source is the API's own `http_requests_total` counter from `/metrics`,
not `istio_requests_total`. Selectors are `namespace=api`, `job=api-lab3`,
`pod=~"api-v2-.*"`, `method=POST`, `path=/order`. The shared ServiceMonitor
scrapes both versions under job `api-lab3`; the pod name distinguishes v2.
`API_METRICS_JOB` and `V2_DEPLOYMENT` can override these selectors.
The numerator counts `code=~"5.."`, and the denominator counts all order attempts.
Retries that reach the backend are counted as separate attempts, including
errors hidden from the client. Health probes and `/version` are excluded.
No order traffic (including only `/version` traffic) means no promotion.
Mesh-generated errors that never reach API are not measured by this gate.
Use FLAKY=false on v1 and FAIL=true/false on v2 for bad/good release tests.

From the repository root, with a Prometheus port-forward running:

```bash
kubectl port-forward -n observability svc/kube-prom-stack-kube-prome-prometheus 9090:9090
# In another terminal:
PROMETHEUS_URL=http://127.0.0.1:9090 bash lecture-6-service-mesh/gate/gate.sh
```

The template deliberately contains routing weights only, without retries.
It manages the full VirtualService spec: do not add unrelated routes to that object.
Only change the template's weights through this script while it runs; Helmfile
can restore chart values on the next apply. The initial VirtualService and
DestinationRule remain chart templates.

For execution in a Pod, package these files and dependencies into an image,
and grant its ServiceAccount namespace-scoped `get`, `patch` and `update`
permissions for VirtualService `api-route`. The resource must already exist.
Allow network access to Prometheus and the Kubernetes API. The gate does not
deploy versions, generate load, or configure Prometheus scraping.

This error-only policy intentionally omits the latency check requested in Lab 6 Part 0;
latency can still be shown separately on the dashboard.
