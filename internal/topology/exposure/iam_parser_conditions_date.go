// SPDX-License-Identifier: Apache-2.0

package exposure

// iam_parser_conditions_date.go holds the Date* and IP operator
// evaluators. Dates parse as RFC3339 first then as epoch seconds
// (float). IP operators parse spec values as CIDR blocks (falling back
// to single IPs) and use net.ParseCIDR / net.ParseIP.

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// evalDateOperator dispatches the 6 date comparison operators. Parse
// failures fall through as a false result (conservative).
func evalDateOperator(op string, specValues, ctxValues []string) bool {
	for _, cv := range ctxValues {
		ctxTime, ok := parseIAMDate(cv)
		if !ok {
			return false
		}
		for _, sv := range specValues {
			specTime, ok := parseIAMDate(sv)
			if !ok {
				continue
			}
			if compareDate(op, ctxTime, specTime) {
				return true
			}
		}
	}
	return false
}

// compareDate returns true if ctxTime op specTime holds. Equality is
// whole-second truncated to match AWS IAM grammar (which documents
// whole-second resolution for Date* operators).
func compareDate(op string, ctxTime, specTime time.Time) bool {
	ctxTime = ctxTime.UTC().Truncate(time.Second)
	specTime = specTime.UTC().Truncate(time.Second)
	switch op {
	case "DateEquals":
		return ctxTime.Equal(specTime)
	case "DateNotEquals":
		return !ctxTime.Equal(specTime)
	case "DateLessThan":
		return ctxTime.Before(specTime)
	case "DateLessThanEquals":
		return ctxTime.Before(specTime) || ctxTime.Equal(specTime)
	case "DateGreaterThan":
		return ctxTime.After(specTime)
	case "DateGreaterThanEquals":
		return ctxTime.After(specTime) || ctxTime.Equal(specTime)
	}
	return false
}

// parseIAMDate tries RFC3339 first, then ISO8601-ish variants, then
// epoch seconds. Returns (zero, false) on total failure.
func parseIAMDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil {
		wholeSecs := int64(secs)
		nanos := int64((secs - float64(wholeSecs)) * 1e9)
		return time.Unix(wholeSecs, nanos).UTC(), true
	}
	return time.Time{}, false
}

// evalIPOperator dispatches IpAddress and NotIpAddress. Spec values are
// CIDRs (e.g. "10.0.0.0/16") or single IPs (e.g. "192.0.2.1", which is
// treated as a /32 or /128). ctxValues are the dotted-quad IP strings
// resolved from ConditionContext.SourceIP or Extras.
func evalIPOperator(op string, specValues, ctxValues []string) bool {
	match := anyIPMatches(specValues, ctxValues)
	switch op {
	case "IpAddress":
		return match
	case "NotIpAddress":
		return !match
	}
	return false
}

// anyIPMatches returns true if ANY ctx IP falls inside ANY spec CIDR.
// Malformed spec entries are skipped rather than failing the whole block
// (matches PMapper's conservative tolerance for mixed IPv4/IPv6 blocks).
func anyIPMatches(specValues, ctxValues []string) bool {
	for _, cv := range ctxValues {
		ip := net.ParseIP(strings.TrimSpace(cv))
		if ip == nil {
			continue
		}
		for _, sv := range specValues {
			if ipInSpec(ip, strings.TrimSpace(sv)) {
				return true
			}
		}
	}
	return false
}

// ipInSpec tests ip against a single spec entry which may be a CIDR
// ("10.0.0.0/16") or a bare IP literal (treated as a host-mask match).
func ipInSpec(ip net.IP, spec string) bool {
	if spec == "" {
		return false
	}
	if strings.Contains(spec, "/") {
		_, cidr, err := net.ParseCIDR(spec)
		if err != nil {
			return false
		}
		return cidr.Contains(ip)
	}
	specIP := net.ParseIP(spec)
	if specIP == nil {
		return false
	}
	return specIP.Equal(ip)
}
