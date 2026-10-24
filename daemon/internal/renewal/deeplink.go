package renewal

// Renewal deep-link security review, 2026-10-25: REJECTED.
//
// The renewal research (REPORT.md P1-4, C7/C8) establishes that official
// clients place LoginToken in the purchase URL's _client_login parameter. Its
// Open Questions explicitly leave a valid token's lifetime and single-/multi-
// use behavior [UNVERIFIED]. The evidence therefore does not support promising
// a one-time credential-bearing URL.
//
// More fundamentally, the proposed POST response returns the vendor URL itself.
// After disclosure, opening that URL again requires no daemon request. An
// in-memory hash can suppress reissuance, but cannot observe that replay or
// answer 410; a browser history copy, redirect target, or copied link bypasses
// it. A daemon-hosted one-time redirect would be a different URL contract and
// still could not revoke the vendor token disclosed by the redirect.
//
// AJS_RENEWAL_DEEPLINK and its routes are intentionally not added. The public
// OfficialPurchaseURL remains the supported renewal path. No live-vendor token
// behavior was exercised [UNVERIFIED].
