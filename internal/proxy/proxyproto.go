package proxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"

	"github.com/pires/go-proxyproto"
)

// Opens every PROXY protocol v2 header
var proxyV2Signature = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}

// PROXY v2 command and family bytes from the haproxy spec
const (
	proxyV2CmdProxy  = 0x21 // Version 2, PROXY command
	proxyV2CmdLocal  = 0x20 // Version 2, LOCAL command
	proxyV2FamTCPv4  = 0x11 // AF_INET, SOCK_STREAM
	proxyV2FamTCPv6  = 0x21 // AF_INET6, SOCK_STREAM
	proxyV2FamUnspec = 0x00 // AF_UNSPEC
	proxyV2LenTCPv4  = 12
	proxyV2LenTCPv6  = 36
)

// Sends client IP via PROXY v2, or LOCAL fallback
func WriteProxyV2Header(w io.Writer, clientAddr, listenerAddr net.Addr) error {
	var buf bytes.Buffer
	buf.Write(proxyV2Signature)

	src, sok := clientAddr.(*net.TCPAddr)
	dst, dok := listenerAddr.(*net.TCPAddr)
	if !sok || !dok || src.IP.To16() == nil || dst.IP.To16() == nil {
		buf.WriteByte(proxyV2CmdLocal)
		buf.WriteByte(proxyV2FamUnspec)
		binary.Write(&buf, binary.BigEndian, uint16(0))
		_, err := w.Write(buf.Bytes())
		return err
	}

	buf.WriteByte(proxyV2CmdProxy)
	if src4, dst4 := src.IP.To4(), dst.IP.To4(); src4 != nil && dst4 != nil {
		buf.WriteByte(proxyV2FamTCPv4)
		binary.Write(&buf, binary.BigEndian, uint16(proxyV2LenTCPv4))
		buf.Write(src4)
		buf.Write(dst4)
	} else {
		buf.WriteByte(proxyV2FamTCPv6)
		binary.Write(&buf, binary.BigEndian, uint16(proxyV2LenTCPv6))
		buf.Write(src.IP.To16())
		buf.Write(dst.IP.To16())
	}
	binary.Write(&buf, binary.BigEndian, uint16(src.Port))
	binary.Write(&buf, binary.BigEndian, uint16(dst.Port))
	_, err := w.Write(buf.Bytes())
	return err
}

// Wraps an ingress net.Listener to parse PROXY protocol v1/v2 headers
func WrapIngressListener(base net.Listener, trustedCIDRs []string) (net.Listener, error) {
	var connPolicyFunc proxyproto.ConnPolicyFunc

	if len(trustedCIDRs) > 0 {
		var nets []*net.IPNet
		for _, cidr := range trustedCIDRs {
			_, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, err
			}
			nets = append(nets, ipNet)
		}

		connPolicyFunc = func(connPolicyOptions proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
			tcpAddr, ok := connPolicyOptions.Upstream.(*net.TCPAddr)
			if !ok {
				return proxyproto.REJECT, nil
			}

			for _, n := range nets {
				if n.Contains(tcpAddr.IP) {
					return proxyproto.USE, nil
				}
			}

			return proxyproto.IGNORE, nil
		}
	} else {
		connPolicyFunc = func(connPolicyOptions proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
			return proxyproto.USE, nil
		}
	}

	return &proxyproto.Listener{
		Listener:   base,
		ConnPolicy: connPolicyFunc,
	}, nil
}
