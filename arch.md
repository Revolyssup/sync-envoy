# sync-envoy Architecture

## Provider Pattern

**Provider = Watcher + Updater** with 3 providers:

| Provider | Watcher | Updater |
|----------|---------|---------|
| `kubernetes` | K8s informers for 11 Istio CRD types | Writes `_current.yaml` files to `istioconfigs/` |
| `file` | fsnotify for `_desired.yaml` files | Applies changes to K8s cluster |
| `envoy` | CSDS streaming / admin endpoint / istioctl fallback | Writes timestamped JSON to `envoyconfigs/` |

## Package Structure

```
pkg/
  types/      - Event, Watcher, Updater interfaces
  logging/    - Leveled logger (debug/info/warn/error)
  diff/       - LCS-based unified diff computation
  k8s/        - Client init, CR watcher, CR updater, helpers
  file/       - DesiredFileWatcher, CurrentFileUpdater
  envoy/      - EnvoyWatcher (3-tier fallback), FileUpdater, CSDS client
  provider/   - Provider composition + Registry
```

## Key Features

1. **Clean packages** - All code organized by responsibility
2. **Provider interface** - `Watcher` (produces events) + `Updater` (consumes events)
3. **`_current`/`_desired` suffix** - K8s provider writes `_current.yaml`, file provider watches `_desired.yaml`
4. **`--provider` flag** - `--provider=kubernetes,envoy` to select providers (all ON by default)
5. **Auto-rename** - Creating `httpbin.yaml` auto-renames to `httpbin_desired.yaml` and gets applied
6. **Non-namespaced** - Stored as `istioconfigs/resourcetype/name.yaml`
7. **Diff detection** - All updaters compare against last update, log diff or skip
8. **No `--interval` flag** - Envoy watcher uses CSDS streaming (with `--csds-address`), falls back to admin endpoint polling, then istioctl polling
9. **Timestamps** - Envoy JSON files include `last_updated`, `pod_name`, `namespace`, `config_type` alongside config
10. **28 unit tests** - All passing across all packages

## File Structure

### Kubernetes Provider Flow
- K8s CRs are watched via dynamic informers
- Any change (add/update/delete) emits an event
- Event contains the full CR as YAML
- File updater writes to `istioconfigs/[namespace/]kind/name_current.yaml`

### File Provider Flow
- File watcher monitors `istioconfigs/` for changes
- Plain `.yaml` files auto-renamed to `_desired.yaml`
- Only `_desired.yaml` files processed (ignores `_current.yaml`)
- Events sent to K8s updater which applies to cluster
- Compares with cluster state before updating

### Envoy Provider Flow
- **Strategy 1 (Primary):** CSDS gRPC streaming from istiod
  - Connection to `--csds-address` (e.g., `localhost:15010`)
  - Receives streaming updates about proxy configs
  - Uses protobuf types from go-control-plane
- **Strategy 2 (Fallback):** Admin endpoint polling via kubectl exec
  - Direct pod access to port 15000
  - Polls `/config_dump` endpoint every 5s
  - Fallback if CSDS unavailable
- **Strategy 3 (Final fallback):** istioctl polling
  - Executes `istioctl proxy-config <type> <pod>`
  - Last resort when direct access unavailable

All config writes include timestamp metadata:
```json
{
  "last_updated": "2024-01-15T10:30:00Z",
  "pod_name": "httpbin-abc",
  "namespace": "default",
  "config_type": "cluster",
  "config": { ... actual envoy config ... }
}
```

## CLI Usage

```bash
# Start with all providers
sync-envoy start

# Start with specific providers
sync-envoy start --provider=kubernetes,file

# With custom namespace and workload selector
sync-envoy start -n prod -w app=payment

# With CSDS streaming enabled
sync-envoy start --csds-address=istiod.istio-system:15010

# Set log level
sync-envoy start --log-level=debug

# Stop running process
sync-envoy stop

# Cleanup all generated files
sync-envoy cleanup
```

## Diff Calculation

- All updaters track last written/applied state
- Before each update, compares old vs new using LCS-based algorithm
- If no diff: logs "No diff detected, skipping" and returns
- If diff found: logs unified diff in output for debugging
- Prevents unnecessary cluster operations and file writes
