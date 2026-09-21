package scan

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/kd14/ranger/internal/core"
)

// MDNSName is the scanner name reported by the service-discovery backend.
const MDNSName = "mdns"

const (
	metaService       = "_services._dns-sd._udp"
	discoveryTimeout  = 4 * time.Second
	browseTimeout     = 3 * time.Second
	maxServiceTypes   = 32
	browseConcurrency = 6
)

// seedServices are browsed every cycle regardless of what the meta-query
// returns, because some devices answer a direct query but not the enumeration.
var seedServices = []string{
	"_airplay._tcp",
	"_raop._tcp",
	"_googlecast._tcp",
	"_spotify-connect._tcp",
	"_ipp._tcp",
	"_printer._tcp",
	"_homekit._tcp",
	"_hap._tcp",
	"_companion-link._tcp",
	"_workstation._tcp",
	"_ssh._tcp",
	"_smb._tcp",
	"_http._tcp",
	"_googlezone._tcp",
	"_sonos._tcp",
	"_matter._tcp",
}

// MDNS discovers DNS-SD services on the local link. These are IP-level
// advertisements, so they carry no signal strength, but they reveal AirPlay
// targets, Chromecasts, printers and smart-home hubs that no radio scan names.
type MDNS struct {
	// Interval is the delay between discovery cycles.
	Interval time.Duration
	// Domain defaults to "local.".
	Domain string
}

func NewMDNS() *MDNS {
	return &MDNS{Interval: 30 * time.Second, Domain: "local."}
}

func (m *MDNS) Name() string { return MDNSName }

func (m *MDNS) Check(context.Context) Availability {
	ifaces, err := net.Interfaces()
	if err != nil {
		return Unavailable("cannot enumerate network interfaces: "+err.Error(), "Check host networking permissions.")
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 && ifi.Flags&net.FlagLoopback == 0 {
			return Available()
		}
	}
	return Unavailable("no multicast-capable network interface is up",
		"Connect to a network; mDNS discovery needs a link-local multicast interface.")
}

func (m *MDNS) Run(ctx context.Context, out chan<- core.Observation) error {
	if m.Interval <= 0 {
		m.Interval = 30 * time.Second
	}
	if m.Domain == "" {
		m.Domain = "local."
	}

	for {
		if err := m.cycle(ctx, out); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(m.Interval):
		}
	}
}

// cycle enumerates service types then browses each one.
func (m *MDNS) cycle(ctx context.Context, out chan<- core.Observation) error {
	types := m.discoverTypes(ctx)

	sem := make(chan struct{}, browseConcurrency)
	results := make(chan core.Observation, 64)

	var wg sync.WaitGroup
	for _, service := range types {
		wg.Add(1)
		go func(service string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			m.browse(ctx, service, results)
		}(service)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for obs := range results {
		if !emit(ctx, out, obs) {
			// Drain so the producing goroutines are not left blocked.
			go func() {
				for range results {
				}
			}()
			return nil
		}
	}
	return nil
}

// discoverTypes asks the network which service types exist, merged with the
// seed list and capped so a noisy network cannot explode the browse fan-out.
func (m *MDNS) discoverTypes(ctx context.Context) []string {
	found := make(map[string]struct{}, len(seedServices))
	for _, s := range seedServices {
		found[s] = struct{}{}
	}

	resolver, err := zeroconf.NewResolver(nil)
	if err == nil {
		entries := make(chan *zeroconf.ServiceEntry, 32)
		queryCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
		if err := resolver.Browse(queryCtx, metaService, m.Domain, entries); err == nil {
			for entry := range entries {
				name := strings.TrimSuffix(entry.Instance, "."+m.Domain)
				if isServiceType(name) && len(found) < maxServiceTypes {
					found[name] = struct{}{}
				}
			}
		}
		cancel()
	}

	types := make([]string, 0, len(found))
	for s := range found {
		types = append(types, s)
	}
	sort.Strings(types)
	if len(types) > maxServiceTypes {
		types = types[:maxServiceTypes]
	}
	return types
}

// browse resolves one service type and converts every instance found.
func (m *MDNS) browse(ctx context.Context, service string, out chan<- core.Observation) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return
	}

	entries := make(chan *zeroconf.ServiceEntry, 16)
	browseCtx, cancel := context.WithTimeout(ctx, browseTimeout)
	defer cancel()

	if err := resolver.Browse(browseCtx, service, m.Domain, entries); err != nil {
		return
	}

	for entry := range entries {
		obs := entryToObservation(entry, service)
		select {
		case out <- obs:
		case <-ctx.Done():
			return
		}
	}
}

func entryToObservation(entry *zeroconf.ServiceEntry, service string) core.Observation {
	meta := map[string]string{"service": service}
	if entry.HostName != "" {
		meta["host"] = strings.TrimSuffix(entry.HostName, ".")
	}
	if entry.Port > 0 {
		meta["port"] = strconv.Itoa(entry.Port)
	}
	if addrs := formatAddrs(entry); addrs != "" {
		meta["addresses"] = addrs
	}
	if model := txtValue(entry.Text, "md", "model", "am"); model != "" {
		meta["model"] = model
	}

	name := unescapeDNSSD(entry.Instance)
	if friendly := txtValue(entry.Text, "fn", "n"); friendly != "" {
		name = friendly
	}

	return core.Observation{
		Kind:    core.KindService,
		Addr:    fmt.Sprintf("%s.%s", unescapeDNSSD(entry.Instance), service),
		Name:    sanitizeText(name),
		Vendor:  vendorFromService(service),
		Source:  MDNSName,
		Meta:    meta,
		Seen:    time.Now(),
		HasRSSI: false,
	}
}

// unescapeDNSSD decodes the presentation format DNS-SD uses for instance
// names, where dots, spaces and backslashes arrive escaped and arbitrary bytes
// appear as \DDD.
func unescapeDNSSD(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		if i+3 < len(s) && isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) {
			n := int(s[i+1]-'0')*100 + int(s[i+2]-'0')*10 + int(s[i+3]-'0')
			if n <= 255 {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i+1])
		i++
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func formatAddrs(entry *zeroconf.ServiceEntry) string {
	var parts []string
	for _, ip := range entry.AddrIPv4 {
		parts = append(parts, ip.String())
	}
	for _, ip := range entry.AddrIPv6 {
		parts = append(parts, ip.String())
	}
	if len(parts) > 4 {
		parts = parts[:4]
	}
	return strings.Join(parts, ", ")
}

// txtValue returns the first matching key from a DNS-SD TXT record.
func txtValue(records []string, keys ...string) string {
	for _, key := range keys {
		prefix := key + "="
		for _, rec := range records {
			if len(rec) > len(prefix) && strings.EqualFold(rec[:len(prefix)], prefix) {
				return sanitizeText(rec[len(prefix):])
			}
		}
	}
	return ""
}

// sanitizeText strips control characters from network-supplied strings and
// bounds their length before they reach the registry.
func sanitizeText(s string) string {
	const maxLen = 96
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= maxLen {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func vendorFromService(service string) string {
	switch {
	case strings.HasPrefix(service, "_googlecast"), strings.HasPrefix(service, "_googlezone"):
		return "Google"
	case strings.HasPrefix(service, "_airplay"), strings.HasPrefix(service, "_raop"),
		strings.HasPrefix(service, "_companion-link"):
		return "Apple"
	case strings.HasPrefix(service, "_spotify"):
		return "Spotify"
	case strings.HasPrefix(service, "_sonos"):
		return "Sonos"
	default:
		return ""
	}
}

func isServiceType(name string) bool {
	return strings.HasPrefix(name, "_") &&
		(strings.HasSuffix(name, "._tcp") || strings.HasSuffix(name, "._udp"))
}
