package store

import (
	"errors"
	"fmt"
	"strings"

	"ajsaccountd/internal/model"
)

// Plan catalogue bounds. A plan is admin configuration that becomes a priced
// snapshot on every order, so the bounds keep a hand-edited snapshot from
// describing an unpriceable or unserviceable order.
const (
	maxPlanNameLen     = 80
	maxPlanAmountMinor = int64(1_000_000_000_000)
	planCurrencyLen    = 3
	planIDPrefix       = "plan-"
	// MaxPlans bounds the admin catalogue; it is small configuration, not
	// customer data, so a hard cap keeps the snapshot predictable.
	MaxPlans = 200
)

// Plans returns every plan, newest first, including deactivated ones. The
// customer route filters to active rows; the admin route shows the catalogue.
func (s *Store) Plans() []model.Plan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Plan, len(s.plans))
	for i, plan := range s.plans {
		out[i] = clonePlan(plan)
	}
	return out
}

// Plan returns one plan by id.
func (s *Store) Plan(id string) (model.Plan, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := s.planIndexLocked(strings.TrimSpace(id))
	if idx < 0 {
		return model.Plan{}, false
	}
	return clonePlan(s.plans[idx]), true
}

// CreatePlan validates and prepends one catalogue entry. Active defaults to
// true; an explicit false creates a draft that no customer can order.
func (s *Store) CreatePlan(in model.PlanInput) (model.Plan, error) {
	name, amount, currency, ttl, quota, err := normalizePlanFields(
		in.Name, in.AmountMinor, in.Currency, in.LeaseTTLSeconds, in.QuotaBytes)
	if err != nil {
		return model.Plan{}, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.plans) >= MaxPlans {
		return model.Plan{}, conflictf("plan catalogue is full (%d entries)", MaxPlans)
	}
	now := iso(s.now())
	s.planSeq++
	plan := model.Plan{
		ID:              fmt.Sprintf("%s%03d", planIDPrefix, s.planSeq),
		Active:          active,
		Name:            name,
		AmountMinor:     amount,
		Currency:        currency,
		LeaseTTLSeconds: ttl,
		QuotaBytes:      quota,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.plans = prependCapped(s.plans, plan, MaxPlans)
	s.addAuditLocked(model.SeverityInfo, "operator", "plan.created", plan.Name,
		fmt.Sprintf("%s %s · ttl %ds · quota %d bytes · active %t",
			formatMinor(amount, currency), currency, ttl, quota, active),
		map[string]string{"planId": plan.ID})
	return clonePlan(plan), nil
}

// UpdatePlan applies a validated partial update. Existing orders keep their
// immutable snapshots; only orders created afterwards see the new price.
func (s *Store) UpdatePlan(id string, patch model.PlanPatch) (model.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.planIndexLocked(strings.TrimSpace(id))
	if idx < 0 {
		return model.Plan{}, notFoundf("plan %q not found", id)
	}
	plan := s.plans[idx]

	name := plan.Name
	if patch.Name != nil {
		name = *patch.Name
	}
	amount := plan.AmountMinor
	if patch.AmountMinor != nil {
		amount = *patch.AmountMinor
	}
	currency := plan.Currency
	if patch.Currency != nil {
		currency = *patch.Currency
	}
	ttl := plan.LeaseTTLSeconds
	if patch.LeaseTTLSeconds != nil {
		ttl = *patch.LeaseTTLSeconds
	}
	quota := plan.QuotaBytes
	if patch.QuotaBytes != nil {
		quota = *patch.QuotaBytes
	}
	normalizedName, normalizedAmount, normalizedCurrency, normalizedTTL, normalizedQuota, err :=
		normalizePlanFields(name, amount, currency, ttl, quota)
	if err != nil {
		return model.Plan{}, err
	}

	plan.Name = normalizedName
	plan.AmountMinor = normalizedAmount
	plan.Currency = normalizedCurrency
	plan.LeaseTTLSeconds = normalizedTTL
	plan.QuotaBytes = normalizedQuota
	if patch.Active != nil {
		plan.Active = *patch.Active
	}
	plan.UpdatedAt = iso(s.now())
	s.plans[idx] = plan
	s.addAuditLocked(model.SeverityInfo, "operator", "plan.updated", plan.Name,
		fmt.Sprintf("%s · ttl %ds · quota %d bytes · active %t",
			formatMinor(plan.AmountMinor, plan.Currency), plan.LeaseTTLSeconds,
			plan.QuotaBytes, plan.Active),
		map[string]string{"planId": plan.ID})
	return clonePlan(plan), nil
}

// DeactivatePlan turns a plan off without deleting it. Deactivation affects
// only new orders: every existing order keeps its snapshot and entitlement.
// The operation is idempotent so an admin retry is safe.
func (s *Store) DeactivatePlan(id string) (model.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.planIndexLocked(strings.TrimSpace(id))
	if idx < 0 {
		return model.Plan{}, notFoundf("plan %q not found", id)
	}
	plan := &s.plans[idx]
	if !plan.Active {
		return clonePlan(*plan), nil
	}
	plan.Active = false
	plan.UpdatedAt = iso(s.now())
	s.addAuditLocked(model.SeverityInfo, "operator", "plan.deactivated", plan.Name,
		"new orders refused; existing snapshots unchanged",
		map[string]string{"planId": plan.ID})
	return clonePlan(*plan), nil
}

func (s *Store) planIndexLocked(id string) int {
	for i, plan := range s.plans {
		if plan.ID == id {
			return i
		}
	}
	return -1
}

// normalizePlanFields validates and canonicalizes one complete plan. It is
// shared by create, update and snapshot restore so the three paths cannot
// disagree about what a valid plan is.
func normalizePlanFields(name string, amountMinor int64, currency string, leaseTTLSeconds int, quotaBytes int64) (string, int64, string, int, int64, error) {
	normalizedName := strings.TrimSpace(name)
	switch {
	case normalizedName == "":
		return "", 0, "", 0, 0, invalidf("name is required")
	case len(normalizedName) > maxPlanNameLen:
		return "", 0, "", 0, 0, invalidf("name must be at most %d characters", maxPlanNameLen)
	case strings.ContainsAny(normalizedName, "\r\n\t"):
		return "", 0, "", 0, 0, invalidf("name must be a single line")
	}
	if amountMinor <= 0 || amountMinor > maxPlanAmountMinor {
		return "", 0, "", 0, 0, invalidf("amountMinor must be between 1 and %d", maxPlanAmountMinor)
	}
	normalizedCurrency, err := normalizePlanCurrency(currency)
	if err != nil {
		return "", 0, "", 0, 0, err
	}
	if leaseTTLSeconds < MinLeaseTTLSeconds || leaseTTLSeconds > MaxLeaseTTLSeconds {
		return "", 0, "", 0, 0, invalidf("leaseTtlSeconds must be between %d and %d",
			MinLeaseTTLSeconds, MaxLeaseTTLSeconds)
	}
	if quotaBytes <= 0 {
		return "", 0, "", 0, 0, invalidf("quotaBytes must be positive")
	}
	return normalizedName, amountMinor, normalizedCurrency, leaseTTLSeconds, quotaBytes, nil
}

// normalizePlanCurrency accepts exactly three ASCII letters and stores the
// uppercase ISO-4217 form. It deliberately does not consult a table: a new
// currency code must not require a daemon release, and the amount is always
// carried in minor units beside it.
func normalizePlanCurrency(raw string) (string, error) {
	currency := strings.ToUpper(strings.TrimSpace(raw))
	if len(currency) != planCurrencyLen {
		return "", invalidf("currency must be a three-letter ISO 4217 code")
	}
	for i := range len(currency) {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return "", invalidf("currency must be a three-letter ISO 4217 code")
		}
	}
	return currency, nil
}

// formatMinor renders a minor-unit amount with its currency for an audit
// detail. It never appears on the wire; the wire carries the integer.
func formatMinor(amountMinor int64, currency string) string {
	return fmt.Sprintf("%d %s minor units", amountMinor, currency)
}

// validatePlans checks the restored catalogue before any order is validated.
// Identity problems (an empty or duplicate id) fail the load; a row with
// malformed price/currency/TTL/quota or timestamps is dropped, because an
// invalid plan must never become orderable. Orders keep their own snapshots,
// so dropping a plan row cannot change an existing entitlement.
func (snap *Snapshot) validatePlans() error {
	if len(snap.Plans) > MaxPlans {
		return fmt.Errorf("state snapshot has %d plans (max %d)", len(snap.Plans), MaxPlans)
	}
	seen := make(map[string]struct{}, len(snap.Plans))
	kept := snap.Plans[:0]
	for _, plan := range snap.Plans {
		id := strings.TrimSpace(plan.ID)
		if id == "" {
			return errors.New("state snapshot has a plan without an id")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("state snapshot has duplicate plan %q", id)
		}
		seen[id] = struct{}{}
		name, amount, currency, ttl, quota, err := normalizePlanFields(
			plan.Name, plan.AmountMinor, plan.Currency, plan.LeaseTTLSeconds, plan.QuotaBytes)
		if err != nil || !validOrderTimestamp(plan.CreatedAt) || !validOrderTimestamp(plan.UpdatedAt) {
			continue
		}
		plan.ID = id
		plan.Name = name
		plan.AmountMinor = amount
		plan.Currency = currency
		plan.LeaseTTLSeconds = ttl
		plan.QuotaBytes = quota
		kept = append(kept, plan)
	}
	snap.Plans = kept
	return nil
}

func clonePlan(plan model.Plan) model.Plan { return plan }

func planSnapshot(plan model.Plan) model.PlanSnapshot {
	return model.PlanSnapshot{
		ID:              plan.ID,
		Name:            plan.Name,
		AmountMinor:     plan.AmountMinor,
		Currency:        plan.Currency,
		LeaseTTLSeconds: plan.LeaseTTLSeconds,
		QuotaBytes:      plan.QuotaBytes,
	}
}

// validPlanSnapshot reports whether a restored order's immutable snapshot is
// internally coherent. A row that fails is quarantined for review rather than
// loaded as an entitlement.
func validPlanSnapshot(snapshot model.PlanSnapshot) bool {
	if strings.TrimSpace(snapshot.ID) == "" || strings.TrimSpace(snapshot.Name) == "" {
		return false
	}
	if snapshot.AmountMinor <= 0 || snapshot.AmountMinor > maxPlanAmountMinor {
		return false
	}
	if _, err := normalizePlanCurrency(snapshot.Currency); err != nil {
		return false
	}
	if snapshot.LeaseTTLSeconds < MinLeaseTTLSeconds || snapshot.LeaseTTLSeconds > MaxLeaseTTLSeconds {
		return false
	}
	return snapshot.QuotaBytes > 0
}
