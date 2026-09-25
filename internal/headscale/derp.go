package headscale

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
)

// The embedded DERP relay (issue #28, a follow-up to #27). headscale can run
// a DERP relay of its own on the server_url it already serves, with a STUN
// listener next to it: two nodes that cannot connect directly then relay
// through this host instead of Tailscale's public servers. S offers it as its
// last question, as a minimal splice of the derp: section like every other
// edit it makes:
//
//   - derp.server.enabled, region_id, region_code and region_name (the
//     file's own values kept, headscale's defaults where there are none),
//     stun_listen_addr, automatically_add_embedded_derp_region, and the
//     private key path when the file has none;
//   - derp.server.ipv4 and ipv6 follow server_url: an address literal there
//     is written in, and the documentation addresses headscale's example file
//     ships (198.51.100.1, 2001:db8::1) are emptied, so clients resolve the
//     server_url name instead of dialling an address that is not this host;
//   - derp.urls keeps or drops Tailscale's public map, as chosen.
//
// The relay is reached over TLS on server_url's port: an http server_url
// leaves it unusable by clients (the dialog says so), and STUN needs its UDP
// port open in the host firewall, which readiness then reads and f opens.

// The S relay choices.
const (
	// DERPKeep leaves the derp: section as it is.
	DERPKeep = "keep"
	// DERPEmbeddedPublic enables the embedded relay and keeps Tailscale's
	// public map next to it: readiness says embedded+tailscale-public.
	DERPEmbeddedPublic = "embedded+public"
	// DERPEmbeddedOnly enables it and drops the public map: every relayed
	// packet goes through this host, and there is no fallback relay.
	DERPEmbeddedOnly = "embedded"
	// DERPPublicOnly turns the embedded relay off, with Tailscale's public
	// map listed so there is still a relay.
	DERPPublicOnly = "public"
)

// Defaults of headscale's example configuration, used where the file has
// none of its own.
const (
	DefaultDERPRegionID       = 999
	DefaultDERPRegionCode     = "headscale"
	DefaultDERPRegionName     = "Headscale Embedded DERP"
	DefaultDERPSTUNListenAddr = "0.0.0.0:3478"
	DefaultDERPPrivateKeyPath = "/var/lib/headscale/derp_server_private.key"
	// PublicDERPMapURL is Tailscale's public DERP map, headscale's default
	// derp.urls.
	PublicDERPMapURL = "https://" + PublicDERPMapHost + "/derpmap/default"
)

// DERPConfig is the derp: section as far as S edits it. It is not part of
// --check: Relays and DERPSTUNListenAddr say what matters.
type DERPConfig struct {
	RegionID       int
	RegionCode     string
	RegionName     string
	PrivateKeyPath string
	IPv4, IPv6     string
	// AutoAddRegion is automatically_add_embedded_derp_region, nil when the
	// key is absent (headscale's default is true).
	AutoAddRegion *bool
	// URLs is derp.urls, nil when the key is absent (which headscale reads
	// as Tailscale's public map); Paths is derp.paths.
	URLs  []string
	Paths []string
}

// DERPSettings is S's relay answer.
type DERPSettings struct {
	Mode string
	// STUNListenAddr is derp.server.stun_listen_addr, for the embedded modes.
	STUNListenAddr string
}

// Embedded reports whether the choice runs headscale's own relay.
func (s DERPSettings) Embedded() bool {
	return s.Mode == DERPEmbeddedPublic || s.Mode == DERPEmbeddedOnly
}

// Validate checks the answer on its own.
func (s DERPSettings) Validate() error {
	switch s.Mode {
	case DERPKeep, DERPPublicOnly:
		return nil
	case DERPEmbeddedPublic, DERPEmbeddedOnly:
		if problem := ListenAddrProblem(s.STUNListenAddr); problem != "" {
			return fmt.Errorf("not a valid stun_listen_addr: %s", problem)
		}
		return nil
	}
	return fmt.Errorf("not a relay choice: %q", s.Mode)
}

// Edits turns the relay answer into the keys to set, against the file's
// current derp: section and the server_url being written.
func (s DERPSettings) Edits(cp ControlPlane, serverURL string) ([]ConfigEdit, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	d := cp.DERP
	set := func(value string, keys ...string) ConfigEdit {
		return ConfigEdit{Path: append([]string{"derp"}, keys...), Value: value}
	}
	var edits []ConfigEdit
	switch s.Mode {
	case DERPKeep:
		return nil, nil
	case DERPPublicOnly:
		edits = append(edits, set(YAMLBool(false), "server", "enabled"))
		if d.URLs != nil && !slices.ContainsFunc(d.URLs, IsPublicDERPMap) && len(d.Paths) == 0 &&
			!hasCustom(d.URLs) {
			// Without the embedded relay and with no map at all, nodes
			// would have no relay: the public map comes back.
			edits = append(edits, set(YAMLList([]string{PublicDERPMapURL}), "urls"))
		}
		return edits, nil
	}

	regionID := d.RegionID
	if regionID <= 0 {
		regionID = DefaultDERPRegionID
	}
	regionCode, regionName := d.RegionCode, d.RegionName
	if regionCode == "" {
		regionCode = DefaultDERPRegionCode
	}
	if regionName == "" {
		regionName = DefaultDERPRegionName
	}
	edits = append(edits,
		set(YAMLBool(true), "server", "enabled"),
		set(strconv.Itoa(regionID), "server", "region_id"),
		set(YAMLString(regionCode), "server", "region_code"),
		set(YAMLString(regionName), "server", "region_name"),
		set(YAMLString(s.STUNListenAddr), "server", "stun_listen_addr"),
		set(YAMLBool(true), "server", "automatically_add_embedded_derp_region"))
	if d.PrivateKeyPath == "" {
		edits = append(edits, set(DefaultDERPPrivateKeyPath, "server", "private_key_path"))
	}
	ipv4, ipv6 := DERPAddresses(serverURL, d.IPv4, d.IPv6)
	if ipv4 != d.IPv4 {
		edits = append(edits, ipEdit(ipv4, "ipv4"))
	}
	if ipv6 != d.IPv6 {
		edits = append(edits, ipEdit(ipv6, "ipv6"))
	}

	urls := d.URLs
	switch {
	case s.Mode == DERPEmbeddedOnly:
		kept := []string{}
		for _, u := range urls {
			if !IsPublicDERPMap(u) {
				kept = append(kept, u)
			}
		}
		if urls == nil || len(kept) != len(urls) {
			edits = append(edits, set(YAMLList(kept), "urls"))
		}
	case urls != nil && !slices.ContainsFunc(urls, IsPublicDERPMap):
		edits = append(edits, set(YAMLList(append(slices.Clone(urls), PublicDERPMapURL)), "urls"))
	}
	return edits, nil
}

// hasCustom reports whether a derp.urls list names a map of the operator's.
func hasCustom(urls []string) bool {
	return slices.ContainsFunc(urls, func(u string) bool { return !IsPublicDERPMap(u) })
}

// ipEdit writes one of the relay's addresses: an address, or empty where the
// file has one to clear (an absent key needs no line).
func ipEdit(value, key string) ConfigEdit {
	edit := ConfigEdit{Path: []string{"derp", "server", key}, Value: YAMLString(value)}
	if value == "" {
		edit.ClearOnly = true
	}
	return edit
}

// DERPAddresses works out derp.server.ipv4 and ipv6 from server_url: an
// address literal there is the relay's address of its family; an address the
// file already has is kept unless it is one of the documentation addresses
// headscale's example ships (RFC 5737, RFC 3849), which are emptied, so the
// clients resolve the server_url name. An address of the other family than a
// literal server_url host is kept as it is.
func DERPAddresses(serverURL, ipv4, ipv6 string) (string, string) {
	if IsDocumentationAddress(ipv4) || !validFamily(ipv4, true) {
		ipv4 = ""
	}
	if IsDocumentationAddress(ipv6) || !validFamily(ipv6, false) {
		ipv6 = ""
	}
	if addr, err := netip.ParseAddr(URLHost(serverURL)); err == nil {
		if addr.Unmap().Is4() {
			ipv4 = addr.Unmap().String()
		} else {
			ipv6 = addr.String()
		}
	}
	return ipv4, ipv6
}

// validFamily reports whether s is empty or an address of the family asked.
func validFamily(s string, four bool) bool {
	if s == "" {
		return true
	}
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Unmap().Is4() == four
}

// documentationPrefixes are the address blocks reserved for examples.
var documentationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// IsDocumentationAddress reports whether s is an address reserved for
// documentation, which no host on a network really has.
func IsDocumentationAddress(s string) bool {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range documentationPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// STUNPort is the UDP port the embedded relay's STUN listener binds, 0 when
// the relay is off or the address names none.
func STUNPort(cp ControlPlane) int {
	if !cp.DERPEmbedded {
		return 0
	}
	return ListenPort(cp.DERPSTUNListenAddr)
}
