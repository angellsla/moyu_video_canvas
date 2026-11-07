package lansharing

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestDirectedBroadcastUsesOnlyLocalIPv4Subnet(t *testing.T) {
	for _, test := range []struct {
		address string
		mask    int
		want    string
	}{
		{address: "192.168.242.1", mask: 24, want: "192.168.242.255"},
		{address: "10.70.60.24", mask: 20, want: "10.70.63.255"},
		{address: "192.168.242.1", mask: 31, want: ""},
	} {
		got := directedBroadcast(net.ParseIP(test.address), net.CIDRMask(test.mask, 32))
		if test.want == "" && got != nil || test.want != "" && (got == nil || got.String() != test.want) {
			t.Fatalf("broadcast for %s/%d = %v, want %q", test.address, test.mask, got, test.want)
		}
	}
}

func TestBroadcastTargetKeepsSourceInterfaceForSameSubnet(t *testing.T) {
	address := &net.IPNet{IP: net.ParseIP("192.168.242.1"), Mask: net.CIDRMask(24, 32)}
	first, ok := broadcastTargetFor(PrivateEndpoint{Address: net.ParseIP("192.168.242.1")}, []net.Addr{address})
	if !ok || first.source.String() != "192.168.242.1" || first.destination.IP.String() != "192.168.242.255" {
		t.Fatalf("first adapter broadcast target = %#v, accepted=%t", first, ok)
	}
	secondAddress := &net.IPNet{IP: net.ParseIP("192.168.242.2"), Mask: net.CIDRMask(24, 32)}
	second, ok := broadcastTargetFor(PrivateEndpoint{Address: net.ParseIP("192.168.242.2")}, []net.Addr{secondAddress})
	if !ok || second.source.String() != "192.168.242.2" || second.destination.IP.String() != first.destination.IP.String() {
		t.Fatalf("second adapter broadcast target = %#v, accepted=%t", second, ok)
	}
}

func TestBroadcastAnnouncementUsesPacketSourceAndValidatesProtocol(t *testing.T) {
	id := strings.Repeat("a", 64)
	packet, err := json.Marshal(broadcastAnnouncement{Service: serviceType, ID: id, Name: "Studio", Port: 41752, Version: protocol})
	if err != nil {
		t.Fatal(err)
	}
	source := &net.UDPAddr{IP: net.ParseIP("192.168.242.130"), Port: 47876}
	peer, ok := peerFromBroadcast(packet, source)
	if !ok || peer.ID != id || peer.Name != "Studio" || peer.Addresses[0] != source.IP.String() || peer.Port != 41752 {
		t.Fatalf("broadcast peer = %#v, accepted=%t", peer, ok)
	}
	for _, invalid := range []struct {
		packet []byte
		source *net.UDPAddr
	}{
		{packet: packet, source: &net.UDPAddr{IP: net.ParseIP("8.8.8.8")}},
		{packet: []byte(`{"service":"other"}`), source: source},
		{packet: []byte(`{"service":"_beeftv-share._tcp","id":"bad","port":41752,"version":"1"}`), source: source},
		{packet: append(packet, make([]byte, broadcastPacketLimit)...), source: source},
	} {
		if _, ok := peerFromBroadcast(invalid.packet, invalid.source); ok {
			t.Fatalf("invalid broadcast was accepted: %s", invalid.packet)
		}
	}
}
