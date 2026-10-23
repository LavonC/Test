package nat

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/stun"
)

// NATType classifies the type of NAT or direct connection.
type NATType string

const (
	NATTypeOpenDirect     NATType = "Open/Direct"
	NATTypeFullCone       NATType = "Full Cone NAT"
	NATTypeRestrictedCone NATType = "Restricted Cone NAT"
	NATTypePortRestricted NATType = "Port Restricted Cone NAT"
	NATTypeSymmetric      NATType = "Symmetric NAT"
	NATTypeUnknown        NATType = "Unknown"
)

// RFC 6598: Carrier-Grade NAT prefix 100.64.0.0/10 (100.64.0.0 to 100.127.255.255)
var cgnatNet = func() *net.IPNet {
	_, ipNet, _ := net.ParseCIDR("100.64.0.0/10")
	return ipNet
}()

// Private IPv4 ranges (RFC 1918)
var privateNets = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
	}
	var nets []*net.IPNet
	for _, cidr := range cidrs {
		_, n, _ := net.ParseCIDR(cidr)
		nets = append(nets, n)
	}
	return nets
}()

// NATReport encapsulates comprehensive dual-stack NAT classification and diagnostics.
type NATReport struct {
	IPv4Supported      bool      `json:"ipv4_supported"`
	IPv6Supported      bool      `json:"ipv6_supported"`
	IsBehindCGNAT      bool      `json:"is_behind_cgnat"`
	NATType            NATType   `json:"nat_type"`
	LocalIPv4s         []string  `json:"local_ipv4s"`
	LocalIPv6s         []string  `json:"local_ipv6s"`
	PublicIPv4         string    `json:"public_ipv4,omitempty"`
	PublicIPv6         string    `json:"public_ipv6,omitempty"`
	HolePunchFeasible  bool      `json:"hole_punch_feasible"`
	RequiresRelay      bool      `json:"requires_relay"`
	FilteredInterfaces []string  `json:"filtered_interfaces,omitempty"`
	Timestamp          time.Time `json:"timestamp"`
}

// IsCGNAT checks if an IP belongs to RFC 6598 Carrier-Grade NAT space (100.64.0.0/10).
func IsCGNAT(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Normalize IPv4-in-IPv6 to 4 bytes
	if ip4 := ip.To4(); ip4 != nil {
		return cgnatNet.Contains(ip4)
	}
	return false
}

// IsPrivateIPv4 checks if an IP belongs to standard private network ranges (RFC 1918).
func IsPrivateIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, n := range privateNets {
		if n.Contains(ip4) {
			return true
		}
	}
	return false
}

// IsVirtualInterface returns true if the network interface name indicates
// a virtual or tunneling adapter (e.g., Hyper-V, WSL, Docker, VPN, WireGuard, TAP).
// Section 6.3: "Hyper-V, WSL, Docker and VPN adapters each generate an unroutable host candidate,
// wasting connectivity checks. Fix: SetInterfaceFilter"
func IsVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	virtualKeywords := []string{
		"vethernet", // Hyper-V & WSL virtual switches on Windows
		"docker",    // Docker bridge
		"vbox",      // VirtualBox
		"vmware",    // VMware
		"tailscale", // Tailscale virtual adapter
		"tap",       // TAP-Windows adapter
		"tun",       // TUN adapter
		"wireguard", // WireGuard adapter
		"wg",        // WireGuard short
		"openvpn",   // OpenVPN
		"teredo",    // Teredo tunneling
		"isatap",    // ISATAP
		"npcap",     // Npcap loopback
		"loopback",  // Loopback
	}

	for _, kw := range virtualKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// InterfaceFilter produces a filter function suitable for Pion SettingEngine.SetInterfaceFilter.
func InterfaceFilter() func(string) bool {
	return func(name string) bool {
		// Return true if interface should be accepted, false to filter out.
		return !IsVirtualInterface(name)
	}
}

// IsTransientSocketError checks for Windows-specific network reset errors
// Section 6.4: SIO_UDP_NETRESET / WSAENETRESET (10052, 10054).
// Hole punching sends to peers before they listen; ICMP unreachables must be treated as transient.
func IsTransientSocketError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for WSAECONNRESET (10054) and WSAENETRESET (10052)
	if strings.Contains(errStr, "10054") ||
		strings.Contains(errStr, "10052") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "network reset") {
		return true
	}
	return false
}

// Classifier coordinates dual-stack STUN probing and NAT characterization.
type Classifier struct {
	mu           sync.Mutex
	stunServers4 []string
	stunServers6 []string
}

// cleanSTUNAddr normalizes a STUN host:port address by removing URL schemes and defaulting the port if omitted.
func cleanSTUNAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}

	lower := strings.ToLower(addr)
	switch {
	case strings.HasPrefix(lower, "stun://") || strings.HasPrefix(lower, "stuns://") ||
		strings.HasPrefix(lower, "turn://") || strings.HasPrefix(lower, "turns://"):
		idx := strings.Index(addr, "://")
		addr = addr[idx+3:]
	case strings.HasPrefix(lower, "stun:") || strings.HasPrefix(lower, "stuns:") ||
		strings.HasPrefix(lower, "turn:") || strings.HasPrefix(lower, "turns:"):
		idx := strings.Index(addr, ":")
		addr = addr[idx+1:]
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}

	// Strip query parameters if present (e.g., ?transport=udp)
	if idx := strings.Index(addr, "?"); idx != -1 {
		addr = strings.TrimSpace(addr[:idx])
		if addr == "" {
			return ""
		}
	}

	// 1. Check if already valid host:port
	if host, port, err := net.SplitHostPort(addr); err == nil {
		return net.JoinHostPort(host, port)
	}

	// 2. Bracketed IPv6 without port: [2001:db8::1] or [::1]
	if strings.HasPrefix(addr, "[") && strings.HasSuffix(addr, "]") {
		inner := addr[1 : len(addr)-1]
		return net.JoinHostPort(inner, "3478")
	}

	// 3. Unbracketed IPv6 without port: 2001:db8::1 or ::1
	if ip := net.ParseIP(addr); ip != nil {
		return net.JoinHostPort(addr, "3478")
	}

	// 4. Unbracketed IPv6 with port: 2001:db8::1:19302
	if lastColon := strings.LastIndex(addr, ":"); lastColon != -1 {
		possibleIP := addr[:lastColon]
		possiblePort := addr[lastColon+1:]
		if ip := net.ParseIP(possibleIP); ip != nil && strings.Contains(possibleIP, ":") {
			if _, err := strconv.Atoi(possiblePort); err == nil {
				return net.JoinHostPort(possibleIP, possiblePort)
			}
		}
	}

	// 5. Hostname or IPv4 without port: stun.l.google.com or 127.0.0.1
	return net.JoinHostPort(addr, "3478")
}

// NewClassifier creates a new NAT classifier.
func NewClassifier(stunServers4 []string, stunServers6 []string) *Classifier {
	if len(stunServers4) == 0 {
		stunServers4 = []string{
			"stun.l.google.com:19302",
			"stun1.l.google.com:19302",
		}
	}
	var cleanV4, cleanV6 []string
	for _, s := range stunServers4 {
		for _, part := range strings.Split(s, ",") {
			c := cleanSTUNAddr(part)
			if c != "" {
				cleanV4 = append(cleanV4, c)
			}
		}
	}
	for _, s := range stunServers6 {
		for _, part := range strings.Split(s, ",") {
			c := cleanSTUNAddr(part)
			if c != "" {
				cleanV6 = append(cleanV6, c)
			}
		}
	}
	return &Classifier{
		stunServers4: cleanV4,
		stunServers6: cleanV6,
	}
}

// ProbeSTUNWithConn sends a STUN binding request over an existing PacketConn to test mapping.
func ProbeSTUNWithConn(ctx context.Context, conn net.PacketConn, serverAddr string) (*stun.XORMappedAddress, error) {
	cleanedAddr := cleanSTUNAddr(serverAddr)
	rAddr, err := net.ResolveUDPAddr("udp", cleanedAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve STUN server %s: %w", serverAddr, err)
	}

	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.WriteTo(msg.Raw, rAddr); err != nil {
		return nil, fmt.Errorf("failed to send STUN request to %s: %w", serverAddr, err)
	}

	buf := make([]byte, 1500)
	deadline := time.Now().Add(3 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_ = conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return nil, err
		}

		res := new(stun.Message)
		res.Raw = make([]byte, n)
		copy(res.Raw, buf[:n])
		if err := res.Decode(); err != nil {
			continue
		}

		if res.TransactionID != msg.TransactionID {
			continue
		}

		if res.Type == stun.BindingSuccess {
			var mappedAddr stun.XORMappedAddress
			if err := mappedAddr.GetFrom(res); err != nil {
				return nil, err
			}
			return &mappedAddr, nil
		}
	}
}

// ProbeSTUN performs a single STUN binding request using the specified network family ("udp4" or "udp6").
func ProbeSTUN(ctx context.Context, network string, serverAddr string) (*stun.XORMappedAddress, error) {
	conn, err := net.ListenPacket(network, ":0")
	if err != nil {
		return nil, fmt.Errorf("failed to create packet socket: %w", err)
	}
	defer conn.Close()

	if udpConn, ok := conn.(*net.UDPConn); ok {
		// Section 6.4: UDP socket buffers may be silently clamped. Call SetReadBuffer.
		_ = udpConn.SetReadBuffer(512 * 1024)
	}

	return ProbeSTUNWithConn(ctx, conn, serverAddr)
}

// Classify gathers interface information, tests IPv4 and IPv6 STUN, and derives the NAT report.
func (c *Classifier) Classify(ctx context.Context) (*NATReport, error) {
	report := &NATReport{
		NATType:   NATTypeUnknown,
		Timestamp: time.Now(),
	}

	// 1. Enumerate and filter physical network interfaces
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			if IsVirtualInterface(iface.Name) {
				report.FilteredInterfaces = append(report.FilteredInterfaces, iface.Name)
				continue
			}

			addrs, aErr := iface.Addrs()
			if aErr != nil {
				continue
			}

			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}

				if ip == nil || ip.IsLoopback() {
					continue
				}

				if ip4 := ip.To4(); ip4 != nil {
					report.LocalIPv4s = append(report.LocalIPv4s, ip4.String())
					report.IPv4Supported = true
					if IsCGNAT(ip4) {
						report.IsBehindCGNAT = true
					}
				} else {
					if ip.IsGlobalUnicast() {
						report.LocalIPv6s = append(report.LocalIPv6s, ip.String())
						report.IPv6Supported = true
					}
				}
			}
		}
	}

	// 2. Dual-stack STUN probe: IPv4 from a single local socket to properly test mapping behavior
	var mappedV4_1, mappedV4_2 *stun.XORMappedAddress
	if len(c.stunServers4) > 0 {
		connV4, err := net.ListenPacket("udp4", ":0")
		if err == nil {
			defer connV4.Close()
			if udpConn, ok := connV4.(*net.UDPConn); ok {
				_ = udpConn.SetReadBuffer(512 * 1024)
			}

			ctxV4, cancelV4 := context.WithTimeout(ctx, 3*time.Second)
			mapped1, err1 := ProbeSTUNWithConn(ctxV4, connV4, c.stunServers4[0])
			cancelV4()
			if err1 == nil && mapped1 != nil {
				mappedV4_1 = mapped1
				report.PublicIPv4 = mapped1.IP.String()
				report.IPv4Supported = true
				if IsCGNAT(mapped1.IP) {
					report.IsBehindCGNAT = true
				}
			}

			// Probe second server FROM THE SAME LOCAL SOCKET to test Endpoint-Independent (Cone) vs Endpoint-Dependent (Symmetric)
			if len(c.stunServers4) > 1 && mappedV4_1 != nil {
				ctxV4_2, cancelV4_2 := context.WithTimeout(ctx, 3*time.Second)
				mapped2, err2 := ProbeSTUNWithConn(ctxV4_2, connV4, c.stunServers4[1])
				cancelV4_2()
				if err2 == nil && mapped2 != nil {
					mappedV4_2 = mapped2
				}
			}
		}
	}

	// 3. Dual-stack STUN probe: IPv6
	if len(c.stunServers6) > 0 {
		connV6, err := net.ListenPacket("udp6", ":0")
		if err == nil {
			defer connV6.Close()
			ctxV6, cancelV6 := context.WithTimeout(ctx, 3*time.Second)
			mapped6, err6 := ProbeSTUNWithConn(ctxV6, connV6, c.stunServers6[0])
			cancelV6()
			if err6 == nil && mapped6 != nil {
				report.PublicIPv6 = mapped6.IP.String()
				report.IPv6Supported = true
			}
		}
	}

	// 4. Determine NAT classification
	if mappedV4_1 == nil && !report.IPv6Supported {
		// No STUN response and no IPv6 -> strict firewall or offline
		report.NATType = NATTypeSymmetric
		report.HolePunchFeasible = false
		report.RequiresRelay = true
		return report, nil
	}

	// Check if any local IP matches public IP
	isDirect := false
	if mappedV4_1 != nil {
		for _, localIP := range report.LocalIPv4s {
			if localIP == mappedV4_1.IP.String() {
				isDirect = true
				break
			}
		}
	}

	if isDirect {
		report.NATType = NATTypeOpenDirect
		report.HolePunchFeasible = true
		report.RequiresRelay = false
	} else if mappedV4_1 != nil && mappedV4_2 != nil {
		// Compare mapped ports across different STUN server destinations from SAME local socket
		if mappedV4_1.IP.Equal(mappedV4_2.IP) && mappedV4_1.Port == mappedV4_2.Port {
			// Mapping is independent of destination -> Cone NAT (Hole punching succeeds)
			report.NATType = NATTypeFullCone
			report.HolePunchFeasible = true
			report.RequiresRelay = false
		} else {
			// Mapping varies by destination IP/Port -> Symmetric NAT
			report.NATType = NATTypeSymmetric
			report.HolePunchFeasible = false
			report.RequiresRelay = true
		}
	} else if report.IsBehindCGNAT {
		// Carrier Grade NAT typically assigns dynamic port bindings per destination
		report.NATType = NATTypePortRestricted
		report.HolePunchFeasible = report.IPv6Supported // If IPv6 is available, direct peer links bypass CGNAT entirely
		report.RequiresRelay = !report.IPv6Supported
	} else {
		// Default to Cone with hole punching feasible
		report.NATType = NATTypeFullCone
		report.HolePunchFeasible = true
		report.RequiresRelay = false
	}

	// Note on IPv6 bypass:
	// "Because 78.6% of residential broadband in India supports native IPv6, direct IPv6 peer links bypass IPv4 CGNAT entirely"
	if report.IPv6Supported {
		report.HolePunchFeasible = true
	}

	return report, nil
}
