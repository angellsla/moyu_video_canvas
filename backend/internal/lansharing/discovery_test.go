package lansharing

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

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

func TestSelectPrivateEndpointsIncludesEveryEligibleAddress(t *testing.T) {
	interfaces := []net.Interface{
		{Index: 1, Name: "ethernet", Flags: net.FlagUp | net.FlagMulticast},
		{Index: 2, Name: "wifi", Flags: net.FlagUp | net.FlagMulticast},
		{Index: 3, Name: "manual-only", Flags: net.FlagUp},
		{Index: 4, Name: "down", Flags: net.FlagMulticast},
		{Index: 5, Name: "loopback", Flags: net.FlagUp | net.FlagLoopback},
	}
	addresses := map[string][]net.Addr{
		"ethernet": {
			&net.IPNet{IP: net.ParseIP("192.168.4.22"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.5.22"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("169.254.4.22"), Mask: net.CIDRMask(16, 32)},
		},
		"wifi": {
			&net.IPNet{IP: net.ParseIP("10.20.30.40"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.4.22"), Mask: net.CIDRMask(24, 32)},
			&net.IPNet{IP: net.ParseIP("8.8.8.8"), Mask: net.CIDRMask(24, 32)},
		},
		"manual-only": {&net.IPNet{IP: net.ParseIP("172.20.1.5"), Mask: net.CIDRMask(16, 32)}},
		"down":        {&net.IPNet{IP: net.ParseIP("10.1.1.1"), Mask: net.CIDRMask(24, 32)}},
		"loopback":    {&net.IPNet{IP: net.ParseIP("10.2.2.2"), Mask: net.CIDRMask(24, 32)}},
	}
	endpoints, err := selectPrivateEndpoints(interfaces, func(iface net.Interface) ([]net.Addr, error) {
		return addresses[iface.Name], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, address string }{
		{"ethernet", "192.168.4.22"},
		{"ethernet", "192.168.5.22"},
		{"wifi", "10.20.30.40"},
		{"manual-only", "172.20.1.5"},
	}
	if len(endpoints) != len(want) {
		t.Fatalf("endpoints = %#v, want %d unique private endpoints", endpoints, len(want))
	}
	for i, endpoint := range endpoints {
		if endpoint.Interface.Name != want[i].name || endpoint.Address.String() != want[i].address {
			t.Fatalf("endpoint %d = %#v, want %s on %s", i, endpoint, want[i].address, want[i].name)
		}
	}
}

func TestSelectPrivateEndpointsHandlesMissingOrFailedAddressLookup(t *testing.T) {
	interfaces := []net.Interface{{Index: 1, Name: "failed", Flags: net.FlagUp}, {Index: 2, Name: "working", Flags: net.FlagUp}}
	if _, err := selectPrivateEndpoints(interfaces, nil); err == nil {
		t.Fatal("nil address lookup should fail")
	}
	endpoints, err := selectPrivateEndpoints(interfaces, func(iface net.Interface) ([]net.Addr, error) {
		if iface.Name == "failed" {
			return nil, errors.New("adapter disappeared")
		}
		return []net.Addr{&net.IPNet{IP: net.ParseIP("10.20.30.40"), Mask: net.CIDRMask(24, 32)}}, nil
	})
	if err != nil || len(endpoints) != 1 || endpoints[0].Address.String() != "10.20.30.40" {
		t.Fatalf("endpoints = %#v, err = %v; want working adapter despite failed lookup", endpoints, err)
	}
	if _, err := selectPrivateEndpoints(interfaces[:1], func(net.Interface) ([]net.Addr, error) { return nil, errors.New("adapter disappeared") }); err == nil {
		t.Fatal("no usable private endpoints should fail")
	}
}

func TestPeerFromEntryRequiresValidPrivatePeerDescriptor(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{Instance: "BeefTV-room._beeftv-share._tcp.local."},
		HostName:      "studio-workstation.local.",
		Port:          44321,
		Text:          []string{"id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "version=" + protocol, "nickname=Studio Editor"},
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
	entry.Text[1] = "version=1"
	if _, ok := peerFromEntry(entry); ok {
		t.Fatal("unsupported protocol version was accepted")
	}
}

func TestPeerFromEntryPreservesAddressesFromMultipleLANs(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		Port:     44321,
		Text:     []string{"id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "version=" + protocol},
		AddrIPv4: []net.IP{net.ParseIP("192.168.4.22"), net.ParseIP("10.20.30.40"), net.ParseIP("169.254.1.5"), net.ParseIP("8.8.8.8")},
	}
	peer, ok := peerFromEntry(entry)
	if !ok {
		t.Fatal("expected a peer on multiple private LANs")
	}
	if len(peer.Addresses) != 2 || peer.Addresses[0] != "192.168.4.22" || peer.Addresses[1] != "10.20.30.40" {
		t.Fatalf("peer addresses = %v, want both private LAN addresses", peer.Addresses)
	}
}

func TestPeerFromEntryFallsBackToHostAndNicknameFitsTXTRecord(t *testing.T) {
	entry := &zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{Instance: "BeefTV-room._beeftv-share._tcp.local."},
		HostName:      "studio-workstation.local.",
		Port:          44321,
		Text:          []string{"id=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "version=" + protocol},
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

func TestBrowseWithRefreshRequeriesAfterTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions := 0
	browseWithRefresh(ctx, time.Millisecond, time.Millisecond, func(browseCtx context.Context) error {
		sessions++
		if sessions == 3 {
			cancel()
		}
		<-browseCtx.Done()
		return browseCtx.Err()
	})
	if sessions != 3 {
		t.Fatalf("browse sessions = %d, want a fresh query after each timeout", sessions)
	}
}

func TestBrowseWithRefreshRetriesFailedResolverAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	browseWithRefresh(ctx, time.Second, time.Millisecond, func(context.Context) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return errors.New("resolver unavailable")
	})
	if calls != 2 {
		t.Fatalf("browse calls = %d, want one retry then stop", calls)
	}
}
