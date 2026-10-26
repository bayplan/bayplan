// Package netguard holds the one rule every outbound dialer of the server
// applies (webhooks, git clones, registry probes, AI providers): the cloud
// metadata services are never a valid destination, whatever DNS says. It is
// checked on the RESOLVED address, in the dialer's control hook, which is the
// check a hostname cannot bypass.
package netguard

import "net"

// metadataHosts are the metadata endpoints that are not link-local and would
// slip past IsLinkLocalUnicast: AWS IMDS over IPv6 and Alibaba Cloud ECS.
var metadataHosts = []net.IP{
	net.ParseIP("fd00:ec2::254"),
	net.ParseIP("100.100.100.200"),
}

// IsMetadata reports whether ip is a link-local address (169.254.0.0/16,
// fe80::/10, including their IPv4-mapped forms) or one of the known
// non-link-local metadata endpoints.
func IsMetadata(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, m := range metadataHosts {
		if ip.Equal(m) {
			return true
		}
	}
	return false
}
