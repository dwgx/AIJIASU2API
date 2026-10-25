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
	// MaxServiceSlots is the hard ceiling on independently operated slots per
	// account. Vendor UseCount can reach 2000, but the daemon serves at most
	// eight concurrent FV sessions for one account (DESIGN-MULTISLOT).
	MaxServiceSlots = 8
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

// SlotCapacity is the display capacity shown for one account:
// min(vendor UseCount, operator AJS_ACCOUNT_SLOTS, MaxServiceSlots). Both
// inputs are normalized, so an absent UseCount and an unset cap both mean one.
// The lease guard deliberately admits only one lease until the multi-slot
// runtime exists; this helper describes the purchased/display allowance only.
func SlotCapacity(useCount, accountSlots int) int {
	capacity := NormalizeUseCount(useCount)
	if limit := NormalizeAccountSlots(accountSlots); limit < capacity {
		capacity = limit
	}
	if capacity > MaxServiceSlots {
		capacity = MaxServiceSlots
	}
	return capacity
}

// EffectiveSlots is the legacy name for SlotCapacity. It returns the same
// min(UseCount, AJS_ACCOUNT_SLOTS, 8) display capacity exposed to the panel;
// the store's lease guard is hard-capped at one independently.
func EffectiveSlots(useCount, accountSlots int) int {
	return SlotCapacity(useCount, accountSlots)
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
