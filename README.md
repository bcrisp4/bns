# BNS: Ben's Name Server

BNS is a caching DNS forwarder with built-in ad-blocking. It is written in Go. It is similar in nature to tools like Pi-hole. It's purpose is to provide network-wide ad blocking at the DNS level for a small private network - Ben's personal network.

## Quickstart

Build:

```bash
make build
```

Run with the sample config (bind to an unprivileged port since :53 needs root):

```bash
./bin/bns serve --config examples/config.example.yaml --listen.udp=127.0.0.1:5353 --listen.tcp=127.0.0.1:5353
```

Then in another terminal:

```bash
dig @127.0.0.1 -p 5353 example.com         # forwarded answer
dig @127.0.0.1 -p 5353 ads.example         # NXDOMAIN
curl http://127.0.0.1:9090/metrics         # Prometheus exposition
```

Reload blocklists in place (no restart):

```bash
pkill -HUP -f 'bin/bns'
```
