# BNS: Ben's Name Server

BNS is a caching DNS forwarder with built-in ad-blocking. It is written in Go. It is similar in nature to tools like Pi-hole. It's purpose is to provide network-wide ad blocking at the DNS level for a small private network - Ben's personal network.

## Quickstart

### Run with Docker (recommended)

A multi-arch image (`linux/amd64`, `linux/arm64`) is published to GitHub
Container Registry on every push to `main` and every `v*` tag:

```
ghcr.io/bcrisp4/bns:latest    # tip of main
ghcr.io/bcrisp4/bns:0.1.0     # pinned release
```

The image ships with a default config that listens on `:5354` inside the
container (the non-root container user cannot bind privileged ports
directly) and fetches the hagezi `pro.txt` blocklist (~471K domains) from
GitHub at startup, caching it to `/var/cache/bns/blocklists`. The cache
is repopulated on a 24h refresh cycle without a restart.

Run it, mapping host `:53` to the container's `:5354` and giving the
fetcher a persistent volume for its cache:

```bash
docker volume create bns-cache
docker run -d \
  --name bns \
  --restart unless-stopped \
  -p 53:5354/udp -p 53:5354/tcp \
  -p 9090:9090 \
  -v bns-cache:/var/cache/bns \
  ghcr.io/bcrisp4/bns:latest
```

First-run UX: until the initial fetch completes (a few seconds on a
healthy link) the matcher is empty and no blocking occurs. Subsequent
starts read the on-disk cache and block immediately.

Then verify from another host (replace `<host>` with the Docker host's
address):

```bash
dig @<host> example.com         # forwarded answer
dig @<host> a-mo.net            # NXDOMAIN (in hagezi pro)
curl http://<host>:9090/metrics | grep bns_
curl http://<host>:9090/readyz
```

If port 53 on the Docker host is already taken (for example by
`systemd-resolved`), stop or reconfigure it first:

```bash
sudo systemctl disable --now systemd-resolved
```

**Overriding the baked configuration.** Three options, layered in this
precedence: flags > env vars > YAML.

```bash
# Bind-mount a replacement config file
docker run ... -v /etc/bns/config.yaml:/etc/bns/config.yaml ghcr.io/bcrisp4/bns:0.1.0

# Override scalars via env vars (double underscore for YAML nesting)
docker run ... -e BNS_LOGGING__LEVEL=debug \
                -e BNS_LOGGING__QUERY_LOG__ENABLED=true \
                ghcr.io/bcrisp4/bns:0.1.0

# Append CLI flags after the image name
docker run ... ghcr.io/bcrisp4/bns:0.1.0 --upstream 8.8.8.8:53 --pprof
```

To reload blocklists in place without restarting the container — picks
up edits to file sources AND triggers an immediate fetcher cycle on HTTP
sources:

```bash
docker kill -s HUP bns
```

### Blocklist sources

Two `type:` values are supported:

```yaml
blocklists:
  refresh_interval: 24h                     # global; applies to all http sources
  cache_dir: /var/cache/bns/blocklists      # writable by uid 65532 in container
  sources:
    - type: http
      name: hagezi-pro                      # required; used as metric label
      url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/domains/pro.txt
    - type: file
      name: custom-blocklist                # required; metric label
      path: /etc/bns/custom-blocklist.txt   # bind-mount to override
```

- `name` is required on every source and must be unique. It is the value
  of the `{source="..."}` label on every `bns_blocklist_*` metric.
- HTTP sources do conditional `GET` (ETag / `If-Modified-Since`); a
  matched conditional returns `304 Not Modified` and the cache is not
  rewritten.
- HTTP fetch failures are fail-open: BNS continues to serve from the
  last good cache (or empty, on cold start with no cache). The
  `bns_blocklist_last_success_timestamp_seconds{source}` gauge supports
  staleness alerts.
- The fetcher dials configured upstream IPs directly to resolve URL
  hostnames, so BNS can fetch its own lists even when it is the sole
  resolver on the network.

The `--blocklist` CLI flag has been removed; use YAML (or `-c
/tmp/test.yaml` for ad-hoc overrides).

Blocklist metrics on `/metrics`:

```
bns_blocklist_fetch_total{source,outcome}            # success|not_modified|failure
bns_blocklist_last_success_timestamp_seconds{source}
bns_blocklist_entries_by_source{source}
bns_blocklist_entries                                 # total, all sources
bns_blocklist_reloads_total{outcome}                  # ok|error
```

### Build from source

```bash
make build
./bin/bns serve --config examples/config.example.yaml \
  --listen.udp=127.0.0.1:5354 --listen.tcp=127.0.0.1:5354
```

Smoke test in another terminal:

```bash
dig @127.0.0.1 -p 5354 example.com         # forwarded answer
dig @127.0.0.1 -p 5354 ads.example         # NXDOMAIN (sample blocklist)
curl http://127.0.0.1:9090/metrics         # Prometheus exposition
```

Reload blocklists in place (no restart):

```bash
pkill -HUP -f 'bin/bns'
```
