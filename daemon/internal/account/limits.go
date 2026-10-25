package account

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Account slot limits. The vendor sells a per-account simultaneous-connection
// allowance (UseCount, 同时连接数) between 1 and 2000. The daemon can serve
// only one FV session per account today, so AJS_ACCOUNT_SLOTS defaults to the
// safe value of one lease per account; raising it records extra leases that
// the runtime cannot serve yet and therefore carries MultiSessionWarning.
const (
	// DefaultAccountSlots is the safe per-account lease cap when
	// AJS_ACCOUNT_SLOTS is unset or invalid.
	DefaultAccountSlots = 1
	// MaxAccountSlots is the vendor's published upper bound for UseCount and
	// the largest accepted AJS_ACCOUNT_SLOTS value.
	MaxAccountSlots = 2000
	// AccountSlotsEnv names the operator's per-account lease-slot cap.
	AccountSlotsEnv = "AJS_ACCOUNT_SLOTS"
	// MultiSessionWarning is the machine-readable warning returned when a
	// lease is created on an account whose effective slot cap is above one.
	// The lease is real, but only the first FV session can be served until
	// per-slot device identities and multiple sessions are implemented.
	MultiSessionWarning = "multi_session_unimplemented"
)

// NormalizeUseCount converts the vendor's UseCount into the persisted range.
// A missing or non-positive value means the vendor omitted the field and
// defaults to one; values above 2000 are clamped to the published maximum.
func NormalizeUseCount(useCount int) int {
	if useCount < 1 {
		return DefaultAccountSlots
	}
	if useCount > MaxAccountSlots {
		return MaxAccountSlots
	}
	return useCount
}

// NormalizeAccountSlots clamps an operator cap into 1..2000. Zero and negative
// values select the safe default of one.
func NormalizeAccountSlots(slots int) int {
	if slots < 1 {
		return DefaultAccountSlots
	}
	if slots > MaxAccountSlots {
		return MaxAccountSlots
	}
	return slots
}

// ParseAccountSlots parses AJS_ACCOUNT_SLOTS. An empty value selects the
// default; a non-integer or out-of-range value is an error so a typo cannot
// silently widen the lease cap.
func ParseAccountSlots(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultAccountSlots, nil
	}
	slots, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q must be an integer", AccountSlotsEnv, raw)
	}
	if slots < 1 || slots > MaxAccountSlots {
		return 0, fmt.Errorf("%s %d must be between 1 and %d", AccountSlotsEnv, slots, MaxAccountSlots)
	}
	return slots, nil
}

// ConfiguredAccountSlots returns the process-wide AJS_ACCOUNT_SLOTS value.
// An invalid value falls back to the safe default because this helper has no
// error channel; ParseAccountSlots remains the validating entry point.
func ConfiguredAccountSlots() int {
	slots, err := ParseAccountSlots(os.Getenv(AccountSlotsEnv))
	if err != nil {
		return DefaultAccountSlots
	}
	return slots
}

// EffectiveSlots is the lease capacity this daemon will actually enforce:
// min(vendor UseCount, operator AJS_ACCOUNT_SLOTS). Both inputs are
// normalized, so an absent UseCount and an unset cap both mean one.
func EffectiveSlots(useCount, accountSlots int) int {
	use := NormalizeUseCount(useCount)
	slots := NormalizeAccountSlots(accountSlots)
	if use < slots {
		return use
	}
	return slots
}

// MultiSessionWarningFor returns MultiSessionWarning when effectiveSlots is
// above one and an empty string otherwise. The warning is intentionally a
// stable code: the dashboard translates it and the API never implies that
// leases beyond the first can be served.
func MultiSessionWarningFor(effectiveSlots int) string {
	if effectiveSlots <= 1 {
		return ""
	}
	return MultiSessionWarning
}
