package lansharing

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"
)

const (
	broadcastDiscoveryPort = 47876
	broadcastInterval      = 10 * time.Second
	broadcastPacketLimit   = 512
)

type broadcastAnnouncement struct {
	Service string `json:"service"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Version string `json:"version"`
}

type broadcastTarget struct {
	source      net.IP
	destination *net.UDPAddr
}

func broadcastTargets(endpoints []PrivateEndpoint) []broadcastTarget {
	targets := make([]broadcastTarget, 0, len(endpoints))
	for _, endpoint := range endpoints {
		addresses, err := endpoint.Interface.Addrs()
		if err != nil {
			continue
		}
		if target, ok := broadcastTargetFor(endpoint, addresses); ok {
			targets = append(targets, target)
		}
	}
	return targets
}

func broadcastTargetFor(endpoint PrivateEndpoint, addresses []net.Addr) (broadcastTarget, bool) {
	for _, address := range addresses {
		ipNet, ok := address.(*net.IPNet)
		if !ok || !ipNet.IP.Equal(endpoint.Address) {
			continue
		}
		broadcast := directedBroadcast(ipNet.IP, ipNet.Mask)
		if broadcast != nil {
			return broadcastTarget{source: endpoint.Address, destination: &net.UDPAddr{IP: broadcast, Port: broadcastDiscoveryPort}}, true
		}
		break
	}
	return broadcastTarget{}, false
}

func directedBroadcast(address net.IP, mask net.IPMask) net.IP {
	ip := address.To4()
	if ip == nil || len(mask) != 4 {
		return nil
	}
	ones, bits := mask.Size()
	if bits != 32 || ones < 1 || ones > 30 {
		return nil
	}
	return net.IPv4(ip[0]|^mask[0], ip[1]|^mask[1], ip[2]|^mask[2], ip[3]|^mask[3])
}

func peerFromBroadcast(packet []byte, source *net.UDPAddr) (Peer, bool) {
	if len(packet) == 0 || len(packet) > broadcastPacketLimit || source == nil || !isPrivateLANAddress(source.IP) {
		return Peer{}, false
	}
	var announcement broadcastAnnouncement
	if err := json.Unmarshal(packet, &announcement); err != nil || announcement.Service != serviceType || announcement.Version != protocol || !validNodeID(announcement.ID) || announcement.Port < 1 || announcement.Port > 65535 {
		return Peer{}, false
	}
	return Peer{ID: strings.ToLower(announcement.ID), Name: normalizeNickname(announcement.Name), Addresses: []string{source.IP.String()}, Port: announcement.Port, Version: protocol}, true
}

// Broadcast is a discovery fallback for links that carry subnet broadcasts but
// do not deliver mDNS multicast. Packets are hints only; pairing verifies TLS identity.
func browseBroadcast(ctx context.Context, endpoints []PrivateEndpoint, nodeID string, nickname func() string, peerPort int, onPeer func(Peer), pairCode func() string) error {
	if ctx == nil || nickname == nil || onPeer == nil || pairCode == nil || !validNodeID(nodeID) || peerPort < 1 || peerPort > 65535 {
		return ErrInvalid
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: broadcastDiscoveryPort})
	if err != nil {
		return err
	}
	defer conn.Close()
	targets := broadcastTargets(endpoints)
	announce := func() {
		packet, err := json.Marshal(broadcastAnnouncement{Service: serviceType, ID: nodeID, Name: nickname(), Port: peerPort, Version: protocol})
		if err != nil || len(packet) > broadcastPacketLimit {
			return
		}
		for _, target := range targets {
			sender, err := net.DialUDP("udp4", &net.UDPAddr{IP: target.source}, target.destination)
			if err != nil {
				continue
			}
			_, _ = sender.Write(packet)
			_ = sender.Close()
		}
	}
	announce()
	ticker := time.NewTicker(broadcastInterval)
	defer ticker.Stop()
	sendCtx, stopSender := context.WithCancel(ctx)
	defer stopSender()
	go func() {
		for {
			select {
			case <-sendCtx.Done():
				return
			case <-ticker.C:
				announce()
			}
		}
	}()
	packet := make([]byte, broadcastPacketLimit+1)
	for ctx.Err() == nil {
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		length, source, err := conn.ReadFromUDP(packet)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return err
		}
		if peer, ok := peerFromBroadcast(packet[:length], source); ok && peer.ID != nodeID {
			onPeer(peer)
		} else if response := pairProbeResponse(packet[:length], source, nodeID, peerPort, pairCode()); response != nil {
			_, _ = conn.WriteToUDP(response, source)
		}
	}
	return nil
}
