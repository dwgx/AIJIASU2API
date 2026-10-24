// Package renewal keeps the vendor's public plan catalogue and derives the
// renewal state shown next to an account.
//
// It is deliberately anonymous and read-only. The only outbound call is a GET
// of the public purchase page: no cookie jar, no account token, no credential
// of any kind. The only link it produces is that same public page. The account
// is selected by the operator after signing in at the vendor site, so the
// daemon never needs to put a credential into a URL.
//
// Prices are vendor list prices, cached in memory for six hours by default.
// Every failure path is soft: the cache serves its last good snapshot, and a
// cold cache serves a small built-in table, so a markup change or an outage
// never breaks the panel.
package renewal

import (
	"fmt"
	"time"
)

// Source says where a PlansSnapshot came from.
type Source string

const (
	// SourceLive is a snapshot fetched and parsed during this call.
	SourceLive Source = "live"
	// SourceCache is a snapshot served from memory. Stale reports whether it
	// outlived the TTL, i.e. whether it is a last-good fallback.
	SourceCache Source = "cache"
	// SourceStatic is the built-in fallback table, used when no live snapshot
	// has ever been cached.
	SourceStatic Source = "static"
)

// Urgency buckets an account's time to expiry.
type Urgency string

const (
	// UrgencyNone means more than SoonDays remain.
	UrgencyNone Urgency = "none"
	// UrgencySoon means the account expires within SoonDays.
	UrgencySoon Urgency = "soon"
	// UrgencyDue means it expires today (zero whole days left).
	UrgencyDue Urgency = "due"
	// UrgencyOverdue means the expiry instant has passed.
	UrgencyOverdue Urgency = "overdue"
)

const (
	// OfficialPurchaseURL is the vendor's public purchase page. It is also the
	// renewal page: the desktop client opens the same URL.
	OfficialPurchaseURL = "https://www.91ajs.com/purchase"
	// DefaultBaseURL is the vendor origin used when Config.BaseURL is empty.
	DefaultBaseURL = "https://www.91ajs.com"
	// SoonDays is the renewal window: an account expiring within this many
	// whole days is "soon".
	SoonDays = 5
	// purchasePath is the public catalogue page, relative to the base URL.
	purchasePath = "/purchase"
)

// Plan is one public list-price row. PriceCNY is a vendor list price in yuan,
// not a quote. AutoRenewPriceCNY is set only when the page publishes an
// auto-renew variant for the same period.
type Plan struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	Months            int    `json:"months"`
	PriceCNY          int64  `json:"priceCny"`
	AutoRenewPriceCNY *int64 `json:"autoRenewPriceCny,omitempty"`
	Trial             bool   `json:"trial,omitempty"`
	Notes             string `json:"notes,omitempty"`
}

// PlansSnapshot is the catalogue plus its provenance. FetchedAt is the UTC
// instant of the last successful live fetch; it is zero for SourceStatic.
type PlansSnapshot struct {
	Plans     []Plan    `json:"plans"`
	FetchedAt time.Time `json:"fetchedAt"`
	Source    Source    `json:"source"`
	Stale     bool      `json:"stale"`
}

// AccountRenewal is one account's expiry state. ExpiresAt echoes the RFC 3339
// input. RenewalURL is always the official purchase page: it never carries an
// account id, a token or any other credential.
type AccountRenewal struct {
	AccountID  string  `json:"accountId"`
	ExpiresAt  string  `json:"expiresAt"`
	DaysLeft   int     `json:"daysLeft"`
	Urgency    Urgency `json:"urgency"`
	RenewalURL string  `json:"renewalUrl"`
}

// RenewalURL returns the vendor's official purchase page.
//
// It intentionally takes no account argument and appends nothing. The vendor
// login token (`_client_login`) is a bearer credential: putting it in a URL
// would leak it into browser history, the Referer header, proxy logs and the
// vendor's own access log, and the public page does not need it. The operator
// signs in at the vendor site and picks the account there.
func RenewalURL() string { return OfficialPurchaseURL }

// Expiry derives the renewal state for one account. now is injected so callers
// and tests share one clock. expiresAt must be RFC 3339; an invalid value is an
// error and no state is returned.
func Expiry(accountID, expiresAt string, now time.Time) (AccountRenewal, error) {
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return AccountRenewal{}, fmt.Errorf("renewal: expiresAt %q is not RFC 3339: %w", expiresAt, err)
	}
	days := daysLeft(expires, now)
	return AccountRenewal{
		AccountID:  accountID,
		ExpiresAt:  expiresAt,
		DaysLeft:   days,
		Urgency:    urgencyFor(days),
		RenewalURL: OfficialPurchaseURL,
	}, nil
}

// daysLeft rounds up for a future expiry and down for a past one, so a
// membership with twelve hours left reports one day and one that lapsed twelve
// hours ago reports minus one. Exactly at the expiry instant it reports zero.
func daysLeft(expires, now time.Time) int {
	remaining := expires.Sub(now)
	if remaining == 0 {
		return 0
	}
	days := remaining / (24 * time.Hour)
	rem := remaining % (24 * time.Hour)
	switch {
	case remaining > 0 && rem > 0:
		days++
	case remaining < 0 && rem < 0:
		days--
	}
	return int(days)
}

// urgencyFor maps whole days to the wire vocabulary: soon at five days or
// fewer, due at zero, overdue below zero.
func urgencyFor(days int) Urgency {
	switch {
	case days < 0:
		return UrgencyOverdue
	case days == 0:
		return UrgencyDue
	case days <= SoonDays:
		return UrgencySoon
	default:
		return UrgencyNone
	}
}
