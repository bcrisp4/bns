// Package chain is the only place that knows the full resolver chain order.
// Everything else takes a resolver.Resolver; this package assembles one.
package chain

import (
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/logging"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/blockstage"
	"github.com/bcrisp4/bns/internal/resolver/cachestage"
	"github.com/bcrisp4/bns/internal/resolver/coalesce"
	"github.com/bcrisp4/bns/internal/resolver/forward"
	"github.com/bcrisp4/bns/internal/resolver/metricstage"
	"github.com/bcrisp4/bns/internal/resolver/qlog"
	"github.com/bcrisp4/bns/internal/upstream"
)

// Deps gathers everything Build needs.
type Deps struct {
	Upstream  upstream.Upstream
	Cache     *cache.LRU
	CacheCfg  config.Cache
	Blocklist *blocklist.Holder
	QueryLog  logging.QueryLog
	Metrics   *metrics.Metrics
}

// Build composes the resolver chain in the order documented in spec §5.4:
//
//	metrics → query-log → blocklist → cache → coalesce → forward
//
// Outermost is metrics; innermost is forward.
func Build(d Deps) resolver.Resolver {
	r := forward.New(d.Upstream)
	r = coalesce.New(r)
	r = cachestage.New(r, d.Cache, d.CacheCfg)
	r = blockstage.New(r, d.Blocklist)
	r = qlog.New(r, d.QueryLog)
	r = metricstage.New(r, d.Metrics)
	return r
}
