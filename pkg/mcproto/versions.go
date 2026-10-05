package mcproto

import "strings"

// Oldest protocol carrying the transfer packet
const TransferProtocol int32 = 766

// One release name with its protocol number
type versionEntry struct {
	name  string
	proto int32
}

// Release names in age order oldest first
var versionEntries = []versionEntry{
	{"1.7.2", 4}, {"1.7.4", 4}, {"1.7.5", 4},
	{"1.7.6", 5}, {"1.7.7", 5}, {"1.7.8", 5}, {"1.7.9", 5}, {"1.7.10", 5},
	{"1.8", 47}, {"1.8.1", 47}, {"1.8.2", 47}, {"1.8.3", 47}, {"1.8.4", 47},
	{"1.8.5", 47}, {"1.8.6", 47}, {"1.8.7", 47}, {"1.8.8", 47}, {"1.8.9", 47},
	{"1.9", 107}, {"1.9.1", 108}, {"1.9.2", 109}, {"1.9.3", 110}, {"1.9.4", 110},
	{"1.10", 210}, {"1.10.1", 210}, {"1.10.2", 210},
	{"1.11", 315}, {"1.11.1", 316}, {"1.11.2", 316},
	{"1.12", 335}, {"1.12.1", 338}, {"1.12.2", 340},
	{"1.13", 393}, {"1.13.1", 401}, {"1.13.2", 404},
	{"1.14", 477}, {"1.14.1", 480}, {"1.14.2", 485}, {"1.14.3", 490}, {"1.14.4", 498},
	{"1.15", 573}, {"1.15.1", 575}, {"1.15.2", 578},
	{"1.16", 735}, {"1.16.1", 736}, {"1.16.2", 751}, {"1.16.3", 753},
	{"1.16.4", 754}, {"1.16.5", 754},
	{"1.17", 755}, {"1.17.1", 756},
	{"1.18", 757}, {"1.18.1", 757}, {"1.18.2", 758},
	{"1.19", 759}, {"1.19.1", 760}, {"1.19.2", 760}, {"1.19.3", 761}, {"1.19.4", 762},
	{"1.20", 763}, {"1.20.1", 763}, {"1.20.2", 764}, {"1.20.3", 765}, {"1.20.4", 765},
	{"1.20.5", 766}, {"1.20.6", 766},
	{"1.21", 767}, {"1.21.1", 767}, {"1.21.2", 768}, {"1.21.3", 768},
	{"1.21.4", 769}, {"1.21.5", 770}, {"1.21.6", 771}, {"1.21.7", 772}, {"1.21.8", 772},
	{"1.21.9", 773}, {"1.21.10", 773}, {"1.21.11", 774},
	{"26.1", 775}, {"26.1.1", 775}, {"26.1.2", 775},
	{"26.2", 776},
}

var protocolByVersion = map[string]int32{}

func init() {
	for _, e := range versionEntries {
		protocolByVersion[e.name] = e.proto
	}
}

// Looks up the protocol number for a release name
func ProtocolForVersion(version string) (int32, bool) {
	proto, ok := protocolByVersion[strings.TrimSpace(version)]
	return proto, ok
}

// Names every release speaking the given protocol
func VersionNamesForProtocol(protocol int32) []string {
	var out []string
	for _, e := range versionEntries {
		if e.proto == protocol {
			out = append(out, e.name)
		}
	}
	return out
}

// Oldest release name speaking the given protocol
func OldestVersionForProtocol(protocol int32) (string, bool) {
	for _, e := range versionEntries {
		if e.proto == protocol {
			return e.name, true
		}
	}
	return "", false
}

// Newest release name speaking the given protocol
func NewestVersionForProtocol(protocol int32) (string, bool) {
	name := ""
	for _, e := range versionEntries {
		if e.proto == protocol {
			name = e.name
		}
	}
	return name, name != ""
}
