package diagnostics

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SSDP multicast target and gateway device types
const (
	ssdpAddr        = "239.255.255.250:1900"
	ssdpSearchIGD   = "urn:schemas-upnp-org:device:InternetGatewayDevice:1"
	ssdpWait        = 1500 * time.Millisecond
	upnpHTTPTimeout = 2 * time.Second
)

// Device description subset naming WAN connection services
type upnpRoot struct {
	Device upnpDevice `xml:"device"`
}

type upnpDevice struct {
	Services []upnpService `xml:"serviceList>service"`
	Devices  []upnpDevice  `xml:"deviceList>device"`
}

type upnpService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

// Asks the router for its WAN address through UPnP IGD
func upnpExternalIP(ctx context.Context) (string, error) {
	location, err := ssdpDiscover(ctx)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: upnpHTTPTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("device description: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", err
	}
	control, serviceType, err := wanControlURL(location, body)
	if err != nil {
		return "", err
	}
	return soapExternalIP(ctx, client, control, serviceType)
}

// Multicasts an M-SEARCH and returns the first LOCATION
func ssdpDiscover(ctx context.Context) (string, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return "", fmt.Errorf("udp socket: %w", err)
	}
	defer conn.Close()
	dst, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return "", err
	}
	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + ssdpAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 1\r\n" +
		"ST: " + ssdpSearchIGD + "\r\n\r\n"
	if _, err := conn.WriteTo([]byte(msg), dst); err != nil {
		return "", fmt.Errorf("multicast send: %w", err)
	}
	deadline := time.Now().Add(ssdpWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetReadDeadline(deadline)
	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return "", errors.New("no UPnP gateway answered")
		}
		if location := ssdpLocation(string(buf[:n])); location != "" {
			return location, nil
		}
	}
}

// LOCATION header from one SSDP response
func ssdpLocation(resp string) string {
	for _, line := range strings.Split(resp, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "location") {
			continue
		}
		return strings.TrimSpace(v)
	}
	return ""
}

// Control url of the WAN IP or PPP connection service
func wanControlURL(location string, body []byte) (string, string, error) {
	var root upnpRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return "", "", fmt.Errorf("device description unreadable: %w", err)
	}
	svc := findWANService(root.Device)
	if svc == nil {
		return "", "", errors.New("gateway lists no WAN connection service")
	}
	base, err := url.Parse(location)
	if err != nil {
		return "", "", err
	}
	ref, err := url.Parse(svc.ControlURL)
	if err != nil {
		return "", "", err
	}
	return base.ResolveReference(ref).String(), svc.ServiceType, nil
}

// Depth first search for a WAN connection service
func findWANService(d upnpDevice) *upnpService {
	for i := range d.Services {
		t := d.Services[i].ServiceType
		if strings.Contains(t, "WANIPConnection") || strings.Contains(t, "WANPPPConnection") {
			return &d.Services[i]
		}
	}
	for _, child := range d.Devices {
		if svc := findWANService(child); svc != nil {
			return svc
		}
	}
	return nil
}

// Calls GetExternalIPAddress on the WAN service
func soapExternalIP(ctx context.Context, client *http.Client, control, serviceType string) (string, error) {
	body := `<?xml version="1.0"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">` +
		`<s:Body><u:GetExternalIPAddress xmlns:u="` + serviceType + `"></u:GetExternalIPAddress></s:Body></s:Envelope>`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, control, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+serviceType+`#GetExternalIPAddress"`)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("soap call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	ip := extractExternalIP(raw)
	if ip == "" {
		return "", fmt.Errorf("gateway answered HTTP %d without an address", resp.StatusCode)
	}
	return ip, nil
}

// Pulls NewExternalIPAddress out of a soap reply
func extractExternalIP(raw []byte) string {
	s := string(raw)
	start := strings.Index(s, "<NewExternalIPAddress>")
	if start < 0 {
		return ""
	}
	start += len("<NewExternalIPAddress>")
	end := strings.Index(s[start:], "</NewExternalIPAddress>")
	if end < 0 {
		return ""
	}
	ip := strings.TrimSpace(s[start : start+end])
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}
