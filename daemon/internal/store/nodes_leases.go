package store

import (
	"fmt"
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
// READY because no session is known to be alive; every other node returns LIVE.
func (s *Store) UndrainNode(id string) (model.ProxyNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.nodeIndexLocked(id)
	if idx < 0 {
		return model.ProxyNode{}, notFoundf("node %q not found", id)
	}
	node := &s.nodes[idx]
	if node.State == model.NodeDown {
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
// latency and egress IP are unchanged; only the probe timestamp moves.
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
		fmt.Sprintf("Canary ok · %d ms · egress verified", node.LatencyMs))
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
		fmt.Sprintf("%s → %s", oldIP, lease.EgressIP))
	return *lease, nil
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
	return *lease, nil
}

// ReleaseLease removes a lease and returns its slot to the node.
func (s *Store) ReleaseLease(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.leaseIndexLocked(id)
	if idx < 0 {
		return notFoundf("lease %q not found", id)
	}
	lease := s.leases[idx]
	if nodeIdx := s.nodeIndexLocked(lease.NodeID); nodeIdx >= 0 && s.nodes[nodeIdx].InUse > 0 {
		s.nodes[nodeIdx].InUse--
	}
	s.leases = append(s.leases[:idx], s.leases[idx+1:]...)
	s.addAuditLocked(model.SeverityInfo, "operator", "lease.release", lease.UserName,
		fmt.Sprintf("Lease on %s released", lease.NodeLabel))
	return nil
}
