package resolver

import "codeberg.org/miekg/dns"

// Outcome classifies the result of a Resolve call into a low-cardinality
// label suitable for metrics and logs. Values: "blocked", "forwarded", "error".
//
// Cache hit vs miss is not distinguished here — both are returned as
// "forwarded" because the resolver chain doesn't propagate the cache stage's
// decision back up to the outer metric stage. Operators infer hit rate
// from bns_upstream_queries_total vs bns_queries_total.
func Outcome(resp *dns.Msg, err error) string {
	switch {
	case err != nil:
		return "error"
	case resp == nil:
		return "error"
	case resp.Rcode == uint16(dns.RcodeNameError):
		return "blocked"
	default:
		return "forwarded"
	}
}
