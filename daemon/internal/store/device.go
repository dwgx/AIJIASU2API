package store

import (
	"fmt"
	"strings"

	"ajsaccountd/internal/model"
)

// DeviceIdentity is the stable, non-secret device identity of one account.
//
// It is generated once per account and reused by every login and FV session,
// so a retry never presents a new device to the vendor. It carries no
// credential. The pool persists it only inside sealed DeviceMaterial; the
// snapshot keeps hashes and public claims.
//
// The field names mirror the vendor claims: UID is _uid, OSDID is _os_did,
// CUID/CSEID are the FV cuid/cseid, and OSP/OSD/OSV/DIM are the FV osp/osd/osv/
// dim values (also _m/_os_dev/_os_ver/_os_dim on the control channel). The
// remaining fields complete the control descriptor so a restored identity
// rebuilds the exact request.
//
// With the device pool enabled the identity lives only in the sealed
// DeviceMaterial file; this type is the decoded value returned by
// AccountDeviceMaterial and the account mirror, never the persisted record.
type DeviceIdentity struct {
	// UID is _uid: 22 characters for the OpenAJS/Android profile, 20 hex for
	// Linux 4.2.3.0 (research/60-dynamic/captures/control.jsonl line 2).
	UID string `json:"uid"`
	// OSDID is _os_did, 16 lowercase hex. The Linux control capture does not
	// carry the field; it is generated for every profile so the record is
	// complete and a profile that does send it keeps one value.
	OSDID string `json:"osdid"`
	// CUID is the FV cuid: equal to UID for Linux 4.2.3.0, equal to OSDID for
	// OpenAJS.
	CUID string `json:"cuid"`
	// CSEID is the FV cseid: "linux" for Linux 4.2.3.0
	// (research/60-dynamic/captures/fvmock-1.log), the device id for OpenAJS.
	CSEID string `json:"cseid"`
	// OSP, OSD, OSV and DIM are the FV osp/osd/osv/dim values.
	OSP string `json:"osp"`
	OSD string `json:"osd"`
	OSV string `json:"osv"`
	DIM string `json:"dim"`

	// Version is _v, or v under the Linux 4.2.3.0 request shape.
	Version string `json:"version"`
	// Site is _site.
	Site string `json:"site"`
	// HW is _os_hw, OSLang is _os_lang and TZMin is _tz. The Linux shape does
	// not send them.
	HW     string `json:"hw,omitempty"`
	OSLang string `json:"osLang,omitempty"`
	TZMin  int    `json:"tzMin,omitempty"`
	// Lang is _lang.
	Lang string `json:"lang,omitempty"`
	// InstMkt is _inst_mkt and Vrf is _vrf.
	InstMkt string `json:"instMkt,omitempty"`
	Vrf     string `json:"vrf,omitempty"`
}

// valid reports whether dev carries the identifiers every request needs.
func (dev DeviceIdentity) valid() bool {
	return strings.TrimSpace(dev.UID) != "" &&
		strings.TrimSpace(dev.OSDID) != "" &&
		strings.TrimSpace(dev.CUID) != "" &&
		strings.TrimSpace(dev.CSEID) != ""
}

// AccountDevice returns the stored device identity of a bound account.
//
// With the pool enabled this is the account mirror rebuilt from sealed
// material; a false result means "no usable bound identity", which is also
// what a missing material entry reports. Callers that must distinguish the two
// use AccountDeviceMaterial and check ErrDeviceMaterialMissing.
func (s *Store) AccountDevice(id string) (DeviceIdentity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dev, ok := s.devices[id]
	return dev, ok
}

// EnsureAccountDevice stores dev for an account that has no device identity and
// returns the stored identity. An existing identity is never replaced, so a
// retry or a concurrent first login keeps the value the vendor saw first; that
// is the stability rule behind the per-account record.
//
// With the device pool enabled it creates a pool record with algorithm
// "legacy/v1" and marks it presented, because the identity was supplied by a
// caller that already had it. Production first logins use BindForLoginSlot; this
// wrapper exists for account import and tests.
//
// dev must carry UID, OSDID, CUID and CSEID; the account must exist.
func (s *Store) EnsureAccountDevice(id string, dev DeviceIdentity) (DeviceIdentity, error) {
	if !dev.valid() {
		return DeviceIdentity{}, invalidf("device identity for %q is missing uid/osdid/cuid/cseid", id)
	}
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.accountIndexLocked(id)
	if idx < 0 {
		return DeviceIdentity{}, notFoundf("account %q not found", id)
	}
	if existing, ok := s.devices[id]; ok {
		return existing, nil
	}
	if recID, ok := s.boundRecordIDLocked(accountSlotKey(id)); ok {
		if rec := s.deviceRecordLocked(recID); rec != nil {
			material, err := s.deviceMaterialLocked(rec)
			if err != nil {
				return DeviceIdentity{}, err
			}
			s.devices[id] = material.Identity
			return material.Identity, nil
		}
	}
	if s.deviceSealer == nil {
		// Pool disabled: keep the pre-pool in-memory behaviour. Snapshot
		// persists this map only in that mode.
		s.devices[id] = dev
		return dev, nil
	}

	account := s.accounts[idx]
	family, err := familyOf(dev, account.Platform)
	if err != nil {
		return DeviceIdentity{}, err
	}
	machineCode, err := randomHex(32)
	if err != nil {
		return DeviceIdentity{}, err
	}
	deviceID := s.newDeviceIDLocked()
	if deviceID == "" {
		return DeviceIdentity{}, fmt.Errorf("store: device id generation failed")
	}
	profile := profileForVersion(dev.Version)
	now := iso(s.now())
	accountKey := deviceAccountKey(family, account.Username)
	slotKey := accountSlotKey(id)
	rec := &DeviceRecord{
		ID: deviceID, State: DeviceAssigned, Platform: family, Profile: profile,
		Algorithm: DeviceAlgorithmLegacy, Fingerprint: deviceFingerprint(machineCode, dev),
		Hashes: deviceHashes(dev, machineCode), Claims: deviceClaims(dev),
		AccountKey:        accountKey,
		AccountKeyAliases: appendDeviceAlias(nil, deviceAccountKey(family, strings.ToLower(account.Username))),
		AssignedAccountID: id, AssignedSlotKey: cloneSlotKey(&slotKey),
		AssignedAt: &now, PresentedAt: &now,
		CreatedAt: now, CreatedBy: "system",
	}
	material := DeviceMaterial{
		DeviceID: deviceID, MachineCode: machineCode, Identity: dev,
		Platform: family, Profile: profile, Algorithm: DeviceAlgorithmLegacy,
		Fingerprint: rec.Fingerprint, CreatedAt: now, PresentedAt: &now,
		AccountKey: accountKey, AccountKeyAliases: rec.AccountKeyAliases,
		AssignedSlotKey: cloneSlotKey(&slotKey),
	}
	if err := s.sealDeviceMaterial(material); err != nil {
		return DeviceIdentity{}, err
	}
	s.ensureSlotRowLocked(slotKey)
	s.devicePool[rec.ID] = rec
	s.deviceMaterial[rec.ID] = cloneDeviceMaterial(material)
	s.setSlotBindingLocked(slotKey, rec.ID)
	s.devices[id] = dev
	s.deviceAppendEventLocked(rec, "created", "system", "from EnsureAccountDevice")
	s.deviceAppendEventLocked(rec, "assigned", "system", "source=legacy")
	s.addAuditLocked(model.SeverityInfo, "system", "device.create", rec.ID,
		"profile="+profile+" algorithm="+DeviceAlgorithmLegacy)
	if err := s.commitLocked(); err != nil {
		return DeviceIdentity{}, err
	}
	return dev, nil
}
