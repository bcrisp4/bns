// Command mockupstream is a minimal DNS server used by the BNS stress
// harness as a substitute upstream. It answers a fixed set of qtypes
// with canned records and is intentionally not configurable beyond a
// few timing knobs. It is not a release artefact.
package main

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"codeberg.org/miekg/dns"
)

func main() {
	udpAddr := flag.String("listen.udp", "127.0.0.1:5355", "UDP listen address")
	tcpAddr := flag.String("listen.tcp", "127.0.0.1:5355", "TCP listen address")
	ttl := flag.Duration("ttl", 300*time.Second, "Positive-answer TTL")
	negTTL := flag.Duration("neg-ttl", 60*time.Second, "SOA minimum (negative-cache) TTL")
	latency := flag.Duration("latency", 0, "Artificial sleep before reply")
	dropRate := flag.Float64("drop-rate", 0.0, "Fraction of queries to silently drop")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	h := NewHandler(HandlerConfig{
		TTL:      *ttl,
		NegTTL:   *negTTL,
		Latency:  *latency,
		DropRate: *dropRate,
	})

	var served atomic.Uint64
	dnsHandler := dns.HandlerFunc(func(ctx context.Context, w dns.ResponseWriter, req *dns.Msg) {
		resp := h.Answer(ctx, req)
		if resp == nil {
			return
		}
		if err := resp.Pack(); err != nil {
			return
		}
		_, _ = io.Copy(w, resp)
		served.Add(1)
	})

	udpConn, err := net.ListenPacket("udp", *udpAddr)
	if err != nil {
		log.Error("bind udp", "addr", *udpAddr, "err", err.Error())
		os.Exit(1)
	}
	tcpLn, err := net.Listen("tcp", *tcpAddr)
	if err != nil {
		log.Error("bind tcp", "addr", *tcpAddr, "err", err.Error())
		os.Exit(1)
	}

	udpReady := make(chan struct{})
	tcpReady := make(chan struct{})
	udpSrv := &dns.Server{
		PacketConn:        udpConn,
		Handler:           dnsHandler,
		NotifyStartedFunc: func(_ context.Context) { close(udpReady) },
	}
	tcpSrv := &dns.Server{
		Listener:          tcpLn,
		Handler:           dnsHandler,
		NotifyStartedFunc: func(_ context.Context) { close(tcpReady) },
	}

	errCh := make(chan error, 2)
	go func() { errCh <- udpSrv.ListenAndServe() }()
	go func() { errCh <- tcpSrv.ListenAndServe() }()
	<-udpReady
	<-tcpReady

	log.Info("mockupstream ready", "udp", *udpAddr, "tcp", *tcpAddr, "ttl", *ttl, "neg_ttl", *negTTL)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sigs:
		log.Info("mockupstream stopping", "signal", s.String())
	case err := <-errCh:
		if err != nil {
			log.Error("listener exited", "err", err.Error())
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	udpSrv.Shutdown(shutdownCtx)
	tcpSrv.Shutdown(shutdownCtx)

	log.Info("mockupstream stopped", "served", served.Load())
}
