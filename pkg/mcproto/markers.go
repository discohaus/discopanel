package mcproto

import "strings"

// Splits forge markers off a handshake address
func SplitHostMarkers(address string) (host, markers string) {
	if i := strings.IndexByte(address, 0); i >= 0 {
		return address[:i], address[i:]
	}
	return address, ""
}
