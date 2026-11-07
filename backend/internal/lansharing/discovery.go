package lansharing

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	serviceType = "_beeftv-share._tcp"
	domain      = "local."
	protocol    = "1"
)

type Peer struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Port      int      `json:"port"`
	Version   string   `json:"version"`
}

type PrivateEndpoint struct {
	Interface net.Interface
	Address   net.IP
}

func FindPrivateEndpoint() (PrivateEndpoint, error) {
	endpoints, err := FindPrivateEndpoints()
	if err != nil {
		return PrivateEndpoint{}, err
	}
	return endpoints[0], nil
}

func FindPrivateEndpoints() ([]PrivateEndpoint, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	return selectPrivateEndpoints(interfaces, func(iface net.Interface) ([]net.Addr, error) {
		return iface.Addrs()
	})
}

func selectPrivateEndpoint(interfaces []net.Interface, addressesFor func(net.Interface) ([]net.Addr, error)) (PrivateEndpoint, error) {
	endpoints, err := selectPrivateEndpoints(interfaces, addressesFor)
	if err != nil {
		return PrivateEndpoint{}, err
	}
	return endpoints[0], nil
}

func selectPrivateEndpoints(interfaces []net.Interface, addressesFor func(net.Interface) ([]net.Addr, error)) ([]PrivateEndpoint, error) {
	if addressesFor == nil {
		return nil, errors.New("LAN interface address lookup is required")
	}
	var multicast, manual []PrivateEndpoint
	seenMulticast := make(map[string]bool)
	seenManual := make(map[string]bool)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := addressesFor(iface)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.To4() == nil || !isPrivateLANAddress(ip) {
				continue
			}
			candidate := PrivateEndpoint{Interface: iface, Address: ip}
			if iface.Flags&net.FlagMulticast != 0 {
				if seenMulticast[ip.String()] {
					continue
				}
				seenMulticast[ip.String()] = true
				multicast = append(multicast, candidate)
			} else {
				if seenManual[ip.String()] {
					continue
				}
				seenManual[ip.String()] = true
				manual = append(manual, candidate)
			}
		}
	}
	endpoints := multicast
	for _, endpoint := range manual {
		if !seenMulticast[endpoint.Address.String()] {
			endpoints = append(endpoints, endpoint)
		}
	}
	if len(endpoints) > 0 {
		return endpoints, nil
	}
	return nil, errors.New("no active private LAN interface is available for sharing")
}

func isPrivateLANAddress(ip net.IP) bool {
	return ip != nil && ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

func Advertise(identity *Identity, endpoint PrivateEndpoint, port int, nickname string) (*zeroconf.Server, error) {
	if identity == nil || identity.ID() == "" || !isPrivateLANAddress(endpoint.Address) || port < 1 || port > 65535 {
		return nil, errors.New("LAN sharing advertisement parameters are invalid")
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "beeftv"
	}
	instance := fmt.Sprintf("BeefTV-%s-%s", identity.ID()[:12], hex.EncodeToString(endpoint.Address.To4()))
	return zeroconf.RegisterProxy(instance, serviceType, domain, port, host+".local.", []string{endpoint.Address.String()}, []string{
		"id=" + identity.ID(),
		"version=" + protocol,
		"nickname=" + mdnsNickname(nickname),
	}, []net.Interface{endpoint.Interface})
}

func mdnsNickname(value string) string {
	value = normalizeNickname(value)
	if value == "" {
		value = systemNickname()
	}
	for len(value) > 245 {
		runes := []rune(value)
		value = string(runes[:len(runes)-1])
	}
	return value
}

func Browse(ctx context.Context, onPeer func(Peer)) error {
	if onPeer == nil {
		return errors.New("LAN peer discovery callback is required")
	}
	interfaces, err := eligibleInterfaces()
	if err != nil {
		return err
	}
	resolver, err := zeroconf.NewResolver(zeroconf.SelectIfaces(interfaces), zeroconf.SelectIPTraffic(zeroconf.IPv4))
	if err != nil {
		return err
	}
	entries := make(chan *zeroconf.ServiceEntry, 32)
	if err := resolver.Browse(ctx, serviceType, domain, entries); err != nil {
		return err
	}
	return consumeBrowseEntries(ctx, entries, onPeer)
}

// A new resolver keeps querying after zeroconf stops probing on its first entry,
// which may be our own advertisement rather than a remote device.
func browseWithRefresh(ctx context.Context, refreshInterval, retryDelay time.Duration, browse func(context.Context) error) {
	for ctx.Err() == nil {
		browseCtx, cancel := context.WithTimeout(ctx, refreshInterval)
		_ = browse(browseCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
		}
	}
}

func consumeBrowseEntries(ctx context.Context, entries <-chan *zeroconf.ServiceEntry, onPeer func(Peer)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry, ok := <-entries:
			if !ok {
				return errors.New("LAN peer discovery stopped")
			}
			peer, ok := peerFromEntry(entry)
			if ok {
				onPeer(peer)
			}
		}
	}
}

func peerFromEntry(entry *zeroconf.ServiceEntry) (Peer, bool) {
	if entry == nil || entry.Port < 1 || entry.Port > 65535 {
		return Peer{}, false
	}
	values := make(map[string]string, len(entry.Text))
	for _, value := range entry.Text {
		key, item, ok := strings.Cut(value, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(item)
		}
	}
	id := strings.ToLower(values["id"])
	version := values["version"]
	if len(id) != 64 || version != protocol {
		return Peer{}, false
	}
	decodedID, err := hex.DecodeString(id)
	if err != nil || len(decodedID) != 32 {
		return Peer{}, false
	}
	addresses := make([]string, 0, len(entry.AddrIPv4))
	for _, address := range entry.AddrIPv4 {
		if isPrivateLANAddress(address) {
			addresses = append(addresses, address.String())
		}
	}
	if len(addresses) == 0 {
		return Peer{}, false
	}
	name := strings.TrimSpace(values["nickname"])
	if name == "" {
		name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(entry.HostName), "."), "."+strings.Trim(domain, "."))
	}
	return Peer{ID: id, Name: name, Addresses: addresses, Port: entry.Port, Version: version}, true
}

func eligibleInterfaces() ([]net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]net.Interface, 0, len(all))
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && isPrivateLANAddress(ip) {
				result = append(result, iface)
				break
			}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("no active private LAN interface supports mDNS")
	}
	return result, nil
}
