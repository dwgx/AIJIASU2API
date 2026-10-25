package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Slot bounds. A slot is one independently operated FV session on an account.
// The vendor sells UseCount between 1 and 2000, but the daemon deliberately
// caps service capacity at eight slots per account (MULTISLOT-PLAN §1).
const (
	MinSlotIndex = 1
	MaxSlotIndex = 8
)

// SlotKey identifies one slot of one account. It is the stable, one-based
// identity used by the store, runtime and API. Slot 1 is the legacy
// account-level binding; higher indices are added only after slot 1 exists.
//
// The struct contains only a string and an int, so it is comparable and can be
// used directly as a map key.
type SlotKey struct {
	AccountID string `json:"accountId"`
	Index     int    `json:"index"`
}

// Valid reports whether the key names a real service slot: a non-empty
// account id and an index in 1..8.
func (k SlotKey) Valid() bool {
	return strings.TrimSpace(k.AccountID) != "" && k.Index >= MinSlotIndex && k.Index <= MaxSlotIndex
}

// String renders the key as "acc-01#2". The format is part of the runtime
// contract: node IDs and tags embed it verbatim.
func (k SlotKey) String() string {
	return fmt.Sprintf("%s#%d", k.AccountID, k.Index)
}

// NodeID is the stable runtime id of one slot's node: "<account>#<N>:<server>".
// It is the only place the format is written; every other package composes a
// node id through it so the switch predicate and the persisted row agree.
func (k SlotKey) NodeID(serverID string) string {
	return k.String() + ":" + serverID
}

// ParseSlotKey parses the "acc-01#2" form produced by String. The account id
// may contain characters other than '#', so the last separator is used.
func ParseSlotKey(raw string) (SlotKey, error) {
	raw = strings.TrimSpace(raw)
	sep := strings.LastIndex(raw, "#")
	if sep <= 0 || sep == len(raw)-1 {
		return SlotKey{}, fmt.Errorf("slot key %q must be in <account>#<index> form", raw)
	}
	index, err := strconv.Atoi(raw[sep+1:])
	if err != nil {
		return SlotKey{}, fmt.Errorf("slot key %q has a non-integer index", raw)
	}
	key := SlotKey{AccountID: strings.TrimSpace(raw[:sep]), Index: index}
	if !key.Valid() {
		return SlotKey{}, fmt.Errorf("slot key %q is outside the 1..%d range", raw, MaxSlotIndex)
	}
	return key, nil
}

// SlotState is the lifecycle state of one account slot. Only these five states
// are persisted; an unknown value makes a snapshot invalid rather than being
// silently coerced.
type SlotState string

const (
	// SlotProvisioning means the slot row exists but no session or gate has
	// been started for it yet. It is the state written by migration and by
	// EnsureAccountSlots.
	SlotProvisioning SlotState = "provisioning"
	// SlotReady means the slot's identity and target are resolved and it can
	// be opened.
	SlotReady SlotState = "ready"
	// SlotLive means one session and one gate currently belong to the slot.
	SlotLive SlotState = "live"
	// SlotDown means the slot is not serving after a failed open or a clean
	// stop; it may be retried under a new generation.
	SlotDown SlotState = "down"
	// SlotQuarantined means repeated failures or vendor kicks stopped automatic
	// retries; an operator must intervene.
	SlotQuarantined SlotState = "quarantined"

	// SlotEmpty and SlotDisabled are compatibility names from the build plan.
	// They map onto the persisted vocabulary instead of adding values the
	// snapshot validator would have to treat as a second spelling of the same
	// state.
	SlotEmpty    = SlotProvisioning
	SlotDisabled = SlotDown
)

// SlotHealth is the optional health marker carried by a slot row. The
// persisted state remains the five-value SlotState vocabulary; health explains
// why a down or quarantined slot stopped.
type SlotHealth string

const (
	SlotHealthUnknown    SlotHealth = "unknown"
	SlotHealthHealthy    SlotHealth = "healthy"
	SlotHealthDegraded   SlotHealth = "degraded"
	SlotHealthVendorKick SlotHealth = "vendor_kick"
)

// Valid reports whether the health marker is known. Empty is accepted because
// health is optional on a freshly provisioned slot.
func (h SlotHealth) Valid() bool {
	switch h {
	case "", SlotHealthUnknown, SlotHealthHealthy, SlotHealthDegraded, SlotHealthVendorKick:
		return true
	default:
		return false
	}
}

// Valid reports whether the state is one of the five persisted values.
func (s SlotState) Valid() bool {
	switch s {
	case SlotProvisioning, SlotReady, SlotLive, SlotDown, SlotQuarantined:
		return true
	default:
		return false
	}
}

// AccountSlot is the persisted contract of one account slot. DeviceID is a
// reference to a DeviceRecord, never a regenerated identity: migration copies
// the account-level binding verbatim and every later retry reuses it.
//
// Generation increases on every controlled session open for the slot; an
// event from an older generation must be ignored by the runtime.
type AccountSlot struct {
	SlotKey
	DeviceID      string     `json:"deviceId,omitempty"`
	ServerID      string     `json:"serverId,omitempty"`
	GatePort      int        `json:"gatePort,omitempty"`
	HTTPPort      int        `json:"httpPort,omitempty"`
	State         SlotState  `json:"state"`
	Health        SlotHealth `json:"health,omitempty"`
	Generation    uint64     `json:"generation"`
	KickCount     int        `json:"kickCount,omitempty"`
	RetryAfter    string     `json:"retryAfter,omitempty"`
	LastAttemptAt string     `json:"lastAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
}

// Validate checks the persisted shape of one slot. Account existence,
// duplicate device ids and duplicate ports are store-level checks because they
// need the whole snapshot.
func (slot AccountSlot) Validate() error {
	if !slot.SlotKey.Valid() {
		return fmt.Errorf("slot key %q is invalid: account id is required and index must be 1..%d", slot.SlotKey.String(), MaxSlotIndex)
	}
	if !slot.State.Valid() {
		return fmt.Errorf("slot %s has unknown state %q", slot.SlotKey.String(), slot.State)
	}
	if slot.GatePort < 0 || slot.GatePort > 65535 {
		return fmt.Errorf("slot %s has invalid gate port %d", slot.SlotKey.String(), slot.GatePort)
	}
	if slot.HTTPPort < 0 || slot.HTTPPort > 65535 {
		return fmt.Errorf("slot %s has invalid http port %d", slot.SlotKey.String(), slot.HTTPPort)
	}
	if slot.GatePort != 0 && slot.GatePort == slot.HTTPPort {
		return fmt.Errorf("slot %s gate and http ports must differ", slot.SlotKey.String())
	}
	if !slot.Health.Valid() {
		return fmt.Errorf("slot %s has unknown health %q", slot.SlotKey.String(), slot.Health)
	}
	if slot.KickCount < 0 {
		return fmt.Errorf("slot %s has a negative kick count", slot.SlotKey.String())
	}
	if !SlotTimestampValid(slot.RetryAfter) {
		return fmt.Errorf("slot %s has an invalid retryAfter timestamp", slot.SlotKey.String())
	}
	if !SlotTimestampValid(slot.LastAttemptAt) {
		return fmt.Errorf("slot %s has an invalid lastAttemptAt timestamp", slot.SlotKey.String())
	}
	return nil
}

// Valid reports whether the slot's persisted shape is well formed.
func (slot AccountSlot) Valid() bool { return slot.Validate() == nil }

// SlotTimestampValid reports whether value is empty or a valid RFC 3339
// timestamp. It is shared by the store's snapshot validation for optional
// slot timestamps.
func SlotTimestampValid(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}
