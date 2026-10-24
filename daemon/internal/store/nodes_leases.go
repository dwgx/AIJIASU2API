package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"ajsaccountd/internal/model"
)

// egressPool mirrors the dashboard mock's rotation pool.
var egressPool = []string{
	"203.0.113.24",
	"198.51.100.44",
	"192.0.2.77",
	"203.0.113.88",
	"198.51.100.91",
}

func nextEgressIP(current string) string {
	for i, ip := range egressPool {
		if ip == current {
			return egressPool[(i+1)%len(egressPool)]
		}
	}
	return egressPool[0]
}

// maxNodeRegionLen bounds the region label mirrored from a vendor server.
const maxNodeRegionLen = 64

// PutNode creates or updates one runtime node. The account must exist, its
// label always follows the account, and a label may not be shared with another
// node because it is also the Resin subscription tag.
//
// The probe- and lease-owned fields (InUse, SuccessRate, LastProbeAt) are
// preserved from the existing row when the input leaves them zero, so a
// runtime refresh cannot clobber a concurrent probe or lease write. A new node
// is appended after the existing rows and gets LastProbeAt=now.
func (s *Store) PutNode(node model.ProxyNode) (model.ProxyNode, error) {
	node, err := normalizeNode(node)
	if err != nil {
		return model.ProxyNode{}, err
	}
	label := node.Label

	s.mu.Lock()
	defer s.mu.Unlock()

	accountIdx := s.accountIndexLocked(node.AccountID)
	if accountIdx < 0 {
		return model.ProxyNode{}, notFoundf("account %q not found", node.AccountID)
	}
	account := s.accounts[accountIdx]

	idx := s.nodeIndexLocked(node.ID)
	if idx >= 0 && s.nodes[idx].AccountID != node.AccountID {
		return model.ProxyNode{}, conflictf("node %q belongs to account %q", node.ID, s.nodes[idx].AccountID)
	}
	for i, other := range s.nodes {
		if i != idx && other.Label == label {
			return model.ProxyNode{}, conflictf("node label %q is already used by %q", label, other.ID)
		}
	}

	node.AccountID = account.ID
	node.AccountLabel = account.Label
	if idx >= 0 {
		existing := s.nodes[idx]
		if node.InUse == 0 {
			node.InUse = existing.InUse
		}
		if node.SuccessRate == 0 {
			node.SuccessRate = existing.SuccessRate
		}
		if node.LastProbeAt == "" {
			node.LastProbeAt = existing.LastProbeAt
		}
		s.nodes[idx] = node
	} else {
		if node.LastProbeAt == "" {
			node.LastProbeAt = iso(s.now())
		}
		s.nodes = append(s.nodes, node)
	}
	s.syncAccountCountsLocked()
	return node, nil
}

// normalizeNode validates one node and trims its string fields. PutNode and
// CommitSwitch share it so a switch cannot write a row PutNode would reject.
func normalizeNode(node model.ProxyNode) (model.ProxyNode, error) {
	id := strings.TrimSpace(node.ID)
	if id == "" || id != node.ID || strings.ContainsAny(id, " \t\r\n") {
		return model.ProxyNode{}, invalidf("node id %q is required and must not contain whitespace", node.ID)
	}
	label := strings.TrimSpace(node.Label)
	if !validNodeLabel(label) {
		return model.ProxyNode{}, invalidf("node %q label %q is not a lowercase Resin tag (a-z, 0-9, single hyphens)", id, node.Label)
	}
	region := strings.TrimSpace(node.Region)
	if region == "" {
		return model.ProxyNode{}, invalidf("node %q region is required", id)
	}
	if len(region) > maxNodeRegionLen {
		return model.ProxyNode{}, invalidf("node %q region must be at most %d characters", id, maxNodeRegionLen)
	}
	if node.GatePort < 0 || node.GatePort > 65535 {
		return model.ProxyNode{}, invalidf("node %q gate port must be between 0 and 65535", id)
	}
	if !validNodeState(node.State) {
		return model.ProxyNode{}, invalidf("node %q state %q is not supported", id, node.State)
	}
	if node.InUse < 0 {
		return model.ProxyNode{}, invalidf("node %q inUse must not be negative", id)
	}
	if node.Capacity < 0 {
		return model.ProxyNode{}, invalidf("node %q capacity must not be negative", id)
	}
	serverID := strings.TrimSpace(node.ServerID)
	if strings.ContainsAny(serverID, " \t\r\n") {
		return model.ProxyNode{}, invalidf("node %q server id must not contain whitespace", id)
	}
	node.ID = id
	node.Label = label
	node.Region = region
	node.ServerID = serverID
	return node, nil
}

// DeleteNode removes a node and every lease that pointed at it. It does not
// touch the node's gate: the caller owns the listener and closes it first.
func (s *Store) DeleteNode(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return notFoundf("node %q not found", id)
	}
	node := s.nodes[idx]
	s.nodes = append(s.nodes[:idx], s.nodes[idx+1:]...)
	kept := s.leases[:0]
	for _, lease := range s.leases {
		if lease.NodeID == id {
			continue
		}
		kept = append(kept, lease)
	}
	s.leases = kept
	s.syncAccountCountsLocked()
	s.addAuditLocked(model.SeverityInfo, "system", "node.delete", node.Label,
		fmt.Sprintf("Node removed · account %s", node.AccountID))
	return nil
}

// validNodeLabel reports whether label is a lowercase Resin tag. It enforces
// the same shape as publisher.ValidTag; the store keeps its own copy so the
// state package stays free of the publisher's Resin dependency.
func validNodeLabel(label string) bool {
	if label == "" {
		return false
	}
	partLen := 0
	for i := range len(label) {
		switch c := label[i]; {
		case c == '-':
			if partLen == 0 {
				return false
			}
			partLen = 0
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			partLen++
		default:
			return false
		}
	}
	return partLen > 0
}

// validNodeState reports whether state is part of the dashboard node
// vocabulary.
func validNodeState(state model.NodeState) bool {
	switch state {
	case model.NodeLive, model.NodeReady, model.NodeStarting, model.NodeDraining, model.NodeDown, model.NodeStale:
		return true
	default:
		return false
	}
}

// DemoteNode records that no in-process gate serves a persisted node after a
// restart: the state becomes STALE, the gate port and egress address are
// cleared, the flow count is zeroed (those flows died with the process) and
// every lease of the node is closed. Only a LIVE or DRAINING row is demoted,
// so a repeated boot is a no-op that writes no second audit row. Exactly one
// node.recovered_stale audit row (actor "system") is written when the row
// changes.
func (s *Store) DemoteNode(id string) (model.ProxyNode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return model.ProxyNode{}, false, notFoundf("node %q not found", id)
	}
	node := &s.nodes[idx]
	if node.State != model.NodeLive && node.State != model.NodeDraining {
		return *node, false, nil
	}
	previous := node.State
	node.State = model.NodeStale
	node.GatePort = 0
	node.EgressIP = ""
	node.InUse = 0
	closed := 0
	kept := s.leases[:0]
	for _, lease := range s.leases {
		if lease.NodeID == id {
			closed++
			continue
		}
		kept = append(kept, lease)
	}
	s.leases = kept
	s.syncAccountCountsLocked()
	s.addAuditLocked(model.SeverityWarn, "system", "node.recovered_stale", node.Label,
		fmt.Sprintf("Restart recovery · no gate bound · %s → STALE · %d leases closed", previous, closed),
		map[string]string{"previousState": string(previous), "leasesClosed": strconv.Itoa(closed)})
	return *node, true, nil
}

// DrainNode moves a node to DRAINING. In-flight flows are left untouched; this
// skeleton has no flow scheduler yet.
func (s *Store) DrainNode(id string) (model.ProxyNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return model.ProxyNode{}, notFoundf("node %q not found", id)
	}
	node := &s.nodes[idx]
	if node.State == model.NodeDown {
		return model.ProxyNode{}, conflictf("node %s is down and cannot be drained", node.Label)
	}
	node.State = model.NodeDraining
	node.LastProbeAt = iso(s.now())
	s.syncAccountCountsLocked()
	s.addAuditLocked(model.SeverityWarn, "operator", "node.drain", node.Label,
		fmt.Sprintf("Drain requested · %d in-flight flows", node.InUse))
	return *node, nil
}

// UndrainNode returns a node to service. A node that was DOWN comes back as
// READY because no session is known to be alive, and a STALE node does too:
// startup recovery closed its gate and its lease, so only a login can make it
// LIVE again. Every other node returns LIVE.
func (s *Store) UndrainNode(id string) (model.ProxyNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return model.ProxyNode{}, notFoundf("node %q not found", id)
	}
	node := &s.nodes[idx]
	if node.State == model.NodeDown || node.State == model.NodeStale {
		node.State = model.NodeReady
	} else {
		node.State = model.NodeLive
	}
	node.LastProbeAt = iso(s.now())
	s.syncAccountCountsLocked()
	s.addAuditLocked(model.SeverityInfo, "operator", "node.undrain", node.Label, "Node returned to service")
	return *node, nil
}

// ProbeNode records a canary probe. There is no real upstream yet, so the
// latency and egress IP are unchanged; only the probe timestamp moves. The
// audit line says exactly that instead of claiming a verified egress.
func (s *Store) ProbeNode(id string) (model.ProxyNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return model.ProxyNode{}, notFoundf("node %q not found", id)
	}
	node := &s.nodes[idx]
	if node.State == model.NodeDown {
		return model.ProxyNode{}, upstreamf("node %s is down; no session to probe", node.Label)
	}
	node.LastProbeAt = iso(s.now())
	s.addAuditLocked(model.SeverityInfo, "system", "node.probe", node.Label,
		"Probe timestamp touched · no upstream probe performed")
	return *node, nil
}

// RotateLease assigns the next egress IP, refreshes the TTL window and mirrors
// the new address onto the node.
func (s *Store) RotateLease(id string) (model.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.leaseIndexLocked(id)
	if idx < 0 {
		return model.Lease{}, notFoundf("lease %q not found", id)
	}
	lease := &s.leases[idx]
	oldIP := lease.EgressIP
	lease.EgressIP = nextEgressIP(lease.EgressIP)
	lease.ExpiresAt = iso(s.now().Add(time.Duration(lease.TTLSeconds) * time.Second))
	lease.Requests += 17
	if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 {
		s.nodes[nodeIdx].EgressIP = lease.EgressIP
	}
	s.addAuditLocked(model.SeverityInfo, "operator", "lease.rotate", lease.UserName,
		fmt.Sprintf("synthetic rotation without Resin · %s → %s", oldIP, lease.EgressIP))
	out := *lease
	s.normalizeLeaseLocked(&out)
	return out, nil
}

// ApplyLeaseRotation records the lease state Resin reported after a rotation:
// the egress IP, the expiry and the request counter. Empty egressIP/expiresAt
// keep the current value; the id, account, user, node and TTL are never
// changed. The node's egress IP mirrors the lease, like RotateLease does.
//
// This is the panel-mode replacement for RotateLease's synthetic next-IP
// logic: the address comes from Resin's GetLease, never from the local pool.
func (s *Store) ApplyLeaseRotation(id, egressIP, expiresAt string, requests int64) (model.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.leaseIndexLocked(id)
	if idx < 0 {
		return model.Lease{}, notFoundf("lease %q not found", id)
	}
	lease := &s.leases[idx]
	oldIP := lease.EgressIP
	if egressIP != "" {
		lease.EgressIP = egressIP
	}
	if expiresAt != "" {
		lease.ExpiresAt = expiresAt
	}
	lease.Requests = requests
	if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 && egressIP != "" {
		s.nodes[nodeIdx].EgressIP = egressIP
	}
	s.addAuditLocked(model.SeverityInfo, "operator", "lease.rotate", lease.UserName,
		fmt.Sprintf("%s → %s", oldIP, lease.EgressIP))
	out := *lease
	s.normalizeLeaseLocked(&out)
	return out, nil
}
