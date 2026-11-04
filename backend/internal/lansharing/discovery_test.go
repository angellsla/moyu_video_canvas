package lansharing

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/grandcat/zeroconf"
)

func TestPrivateLANAddressFiltersNonLANAndLinkLocalTargets(t *testing.T) {
	for _, test := range []struct {
		address string
		want    bool
	}{
		{address: "192.168.1.4", want: true},
		{address: "10.20.30.40", want: true},
		{address: "172.20.1.5", want: true},
		{address: "172.40.1.5", want: false},
		{address: "169.254.1.5", want: false},
		{address: "127.0.0.1", want: false},
		{address: "8.8.8.8", want: false},
		{address: "224.0.0.1", want: false},
	} {
		t.Run(test.address, func(t *testing.T) {
			if got := isPrivateLANAddress(net.ParseIP(test.address)); got != test.want {
				t.Fatalf("isPrivateLANAddress(%s) = %t, want %t", test.address, got, test.want)
			}
		})
	}
}

func TestSelectPrivateEndpointPrefersMulticastButAllowsManualOnlyInterface(t *testing.T) {
	privateAddress := &net.IPNet{IP: net.ParseIP("192.168.4.22"), Mask: net.CIDRMask(24, 32)}
	interfaces := []net.Interface{
		{Index: 1, Name: "manual-only", Flags: net.FlagUp},
		{Index: 2, Name: "multicast", Flags: net.FlagUp | net.FlagMulticast},
	}
	addresses := func(net.Interface) ([]net.Addr, error) {
		return []net.Addr{privateAddress}, nil
	}

	endpoint, err := selectPrivateEndpoint(interfaces, addresses)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Interface.Name != "multicast" || endpoint.Address.String() != "192.168.4.22" {
		t.Fatalf("selected endpoint = %#v, want multicast-capable private interface", endpoint)
	}

	endpoint, err = selectPrivateEndpoint(interfaces[:1], addresses)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Interface.Name != "manual-only" {
		t.Fatalf("selected interface = %q, want non-multicast fallback", endpoint.Interface.Name)
	}
}

func TestPeerFromEntryRequiresValidPrivatePeerDescriptor(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{Instance: "BeefTV-room._beeftv-share._tcp.local."},
		HostName:      "studio-workstation.local.",
		Port:          44321,
		Text:          []string{"id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "version=1", "nickname=Studio Editor"},
		AddrIPv4:      []net.IP{net.ParseIP("192.168.4.22"), net.ParseIP("8.8.8.8")},
	}
	peer, ok := peerFromEntry(entry)
	if !ok {
		t.Fatal("expected valid descriptor to be accepted")
	}
	if peer.ID != entry.Text[0][3:] || peer.Port != entry.Port || len(peer.Addresses) != 1 || peer.Addresses[0] != "192.168.4.22" {
		t.Fatalf("unexpected peer descriptor: %#v", peer)
	}
	if peer.Name != "Studio Editor" {
		t.Fatalf("peer name = %q, want advertised nickname", peer.Name)
	}
	entry.Text[1] = "version=2"
	if _, ok := peerFromEntry(entry); ok {
		t.Fatal("unsupported protocol version was accepted")
	}
}

func TestPeerFromEntryFallsBackToHostAndNicknameFitsTXTRecord(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{Instance: "BeefTV-room._beeftv-share._tcp.local."},
		HostName:      "studio-workstation.local.",
		Port:          44321,
		Text:          []string{"id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "version=1"},
		AddrIPv4:      []net.IP{net.ParseIP("192.168.4.22")},
	}
	peer, ok := peerFromEntry(entry)
	if !ok || peer.Name != "studio-workstation" {
		t.Fatalf("peer = %#v, accepted=%t; want host fallback", peer, ok)
	}
	if got := mdnsNickname(strings.Repeat("名", 80)); len(got) > 245 {
		t.Fatalf("mDNS nickname has %d bytes, want <= 245", len(got))
	}
}

func TestConsumeBrowseEntriesStopsWhenResolverClosesChannel(t *testing.T) {
	entries := make(chan *zeroconf.ServiceEntry)
	close(entries)
	if err := consumeBrowseEntries(context.Background(), entries, func(Peer) {}); err == nil {
		t.Fatal("closed resolver entries should stop discovery")
	}
}
