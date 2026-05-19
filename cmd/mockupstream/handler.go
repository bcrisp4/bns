package main

import (
	"context"
	"math/rand"
	"net/netip"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/rdata"
)

// HandlerConfig holds the runtime knobs for the mock handler.
type HandlerConfig struct {
	TTL      time.Duration
	NegTTL   time.Duration
	Latency  time.Duration
	DropRate float64
}

// Handler answers DNS queries with canned, instant responses. It is the
// upstream substitute used by the stress harness to isolate BNS from
// network and real-recursor variability.
type Handler struct {
	cfg HandlerConfig
	rng *rand.Rand
}

// NewHandler builds a Handler with the given configuration. The PRNG is
// seeded with the current time; reseed inside tests if determinism matters.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		cfg: cfg,
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Answer returns the canned response for req. It returns nil when the
// configured drop-rate triggers, signalling the wire layer to silently
// drop the query (exercises BNS timeout handling). Latency is applied
// before returning if configured.
func (h *Handler) Answer(_ context.Context, req *dns.Msg) *dns.Msg {
	if h.cfg.DropRate > 0 && h.rng.Float64() < h.cfg.DropRate {
		return nil
	}

	if h.cfg.Latency > 0 {
		time.Sleep(h.cfg.Latency)
	}

	resp := &dns.Msg{}
	resp.Response = true
	resp.ID = req.ID
	resp.Question = req.Question

	if len(req.Question) == 0 {
		resp.Rcode = uint16(dns.RcodeRefused)
		return resp
	}

	q := req.Question[0]
	name := q.Header().Name
	ttl := uint32(h.cfg.TTL.Seconds())
	negTTL := uint32(h.cfg.NegTTL.Seconds())

	switch dns.RRToType(q) {
	case dns.TypeA:
		rr := &dns.A{
			Hdr: dns.Header{Name: name, Class: dns.ClassINET, TTL: ttl},
			A:   rdata.A{Addr: netip.MustParseAddr("192.0.2.1")},
		}
		resp.Answer = append(resp.Answer, rr)
	case dns.TypeAAAA:
		rr := &dns.AAAA{
			Hdr:  dns.Header{Name: name, Class: dns.ClassINET, TTL: ttl},
			AAAA: rdata.AAAA{Addr: netip.MustParseAddr("2001:db8::1")},
		}
		resp.Answer = append(resp.Answer, rr)
	case dns.TypeNS:
		rr := &dns.NS{
			Hdr: dns.Header{Name: name, Class: dns.ClassINET, TTL: ttl},
			NS:  rdata.NS{Ns: "ns.mock.invalid."},
		}
		resp.Answer = append(resp.Answer, rr)
	case dns.TypeSOA:
		rr := &dns.SOA{
			Hdr: dns.Header{Name: name, Class: dns.ClassINET, TTL: ttl},
			SOA: rdata.SOA{
				Ns:      "ns.mock.invalid.",
				Mbox:    "hostmaster.mock.invalid.",
				Serial:  1,
				Refresh: 3600,
				Retry:   600,
				Expire:  86400,
				Minttl:  negTTL,
			},
		}
		resp.Answer = append(resp.Answer, rr)
	default:
		resp.Rcode = uint16(dns.RcodeRefused)
	}

	return resp
}
