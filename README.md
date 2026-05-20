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

The image bakes the hagezi `pro.txt` blocklist (~459K domains) and a
default config that listens on `:5354` inside the container (the
non-root container user cannot bind privileged ports directly).

Run it, mapping host `:53` to the container's `:5354`:

```bash
docker run -d \
  --name bns \
  --restart unless-stopped \
  -p 53:5354/udp -p 53:5354/tcp \
  -p 9090:9090 \
  ghcr.io/bcrisp4/bns:0.1.0
```

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

To use a different blocklist file, mount over the baked one:

```bash
docker run ... -v /path/to/your/list.txt:/etc/bns/blocklists/pro.txt ghcr.io/bcrisp4/bns:0.1.0
```

To reload the blocklist in place without restarting the container:

```bash
docker kill -s HUP bns
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
