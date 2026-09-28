package outside

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A small UPnP Internet Gateway Device client: just enough to find the home
// router, ask it for its internet address, and open and close port
// forwards. Most home routers speak this when "UPnP" is on in their settings.

// gateway is a router that answered, and where to send it commands.
type gateway struct {
	location string // its description URL
	control  string // the WANIPConnection (or WANPPPConnection) control URL
	service  string // that service's type, for SOAPAction
	name     string // "NETGEAR R7000", for the panel
}

var searchTargets = []string{
	"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
	"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// routerClient talks to the router only: no redirects (a device on the
// network can't send the panel elsewhere) and no proxy.
var routerClient = &http.Client{
	Timeout:       6 * time.Second,
	Transport:     &http.Transport{Proxy: nil, ResponseHeaderTimeout: 6 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// discover finds the router with SSDP from the local address (so the search
// goes out on the home network), or uses location when it's given.
func discover(ctx context.Context, local netip.Addr, location string) (*gateway, error) {
	if location != "" {
		return describe(ctx, location)
	}
	laddr := &net.UDPAddr{}
	if local.IsValid() {
		laddr.IP = net.IP(local.AsSlice())
	}
	conn, err := net.ListenUDP("udp4", laddr)
	if err != nil {
		return nil, fmt.Errorf("couldn't search for the router: %w", err)
	}
	defer conn.Close()
	group := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}
	for _, st := range searchTargets {
		msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + st + "\r\n\r\n"
		if _, err := conn.WriteToUDP([]byte(msg), group); err != nil {
			return nil, fmt.Errorf("couldn't search for the router: %w", err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	tried := map[string]bool{}
	var lastErr error
	buf := make([]byte, 4096)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		loc := ssdpLocation(buf[:n])
		if loc == "" || tried[loc] || !homeURL(loc, from) {
			continue
		}
		tried[loc] = true
		g, err := describe(ctx, loc)
		if err == nil {
			return g, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no router answered. Turn on UPnP in the router's settings, or forward the ports by hand")
}

// ssdpLocation returns the LOCATION header of an SSDP answer.
func ssdpLocation(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "location") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// homeURL accepts only description URLs on the device that answered, on
// the home network: nothing else on the network can send the panel off to
// fetch other addresses.
func homeURL(loc string, from *net.UDPAddr) bool {
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "http" {
		return false
	}
	a, err := netip.ParseAddr(u.Hostname())
	if err != nil {
		return false
	}
	f, ok := netip.AddrFromSlice(from.IP)
	return ok && a == f.Unmap() && (a.IsPrivate() || a.IsLinkLocalUnicast())
}

type xmlDevice struct {
	FriendlyName string       `xml:"friendlyName"`
	ModelName    string       `xml:"modelName"`
	Services     []xmlService `xml:"serviceList>service"`
	Devices      []xmlDevice  `xml:"deviceList>device"`
}

type xmlService struct {
	Type    string `xml:"serviceType"`
	Control string `xml:"controlURL"`
}

func describe(ctx context.Context, location string) (*gateway, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	res, err := routerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the router didn't answer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the router answered %s", res.Status)
	}
	var root struct {
		URLBase string    `xml:"URLBase"`
		Device  xmlDevice `xml:"device"`
	}
	if err := xml.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&root); err != nil {
		return nil, fmt.Errorf("the router's description isn't readable: %w", err)
	}
	base, _ := url.Parse(location)
	if root.URLBase != "" {
		if b, err := url.Parse(root.URLBase); err == nil && b.Host == base.Host {
			base = b
		}
	}
	var found *xmlService
	var walk func(d *xmlDevice)
	walk = func(d *xmlDevice) {
		for i := range d.Services {
			s := &d.Services[i]
			if found == nil && (strings.Contains(s.Type, ":WANIPConnection:") || strings.Contains(s.Type, ":WANPPPConnection:")) {
				found = s
			}
		}
		for i := range d.Devices {
			walk(&d.Devices[i])
		}
	}
	walk(&root.Device)
	if found == nil {
		return nil, errors.New("the router answered, but doesn't offer port forwarding over UPnP")
	}
	ctl, err := base.Parse(found.Control)
	if err != nil || ctl.Host != base.Host {
		return nil, errors.New("the router's port forwarding address is odd; forward the ports by hand")
	}
	name := strings.TrimSpace(root.Device.FriendlyName)
	if name == "" {
		name = strings.TrimSpace(root.Device.ModelName)
	}
	return &gateway{location: location, control: ctl.String(), service: found.Type, name: name}, nil
}

// upnpError is an error the router sent back.
type upnpError struct {
	Code        int
	Description string
}

func (e *upnpError) Error() string {
	switch e.Code {
	case 718:
		return "the router already forwards this port to another device"
	case 606, 401:
		return "the router refused (UPnP may only be allowed to read, not change, its settings)"
	}
	if e.Description != "" {
		return fmt.Sprintf("the router refused: %s (%d)", e.Description, e.Code)
	}
	return fmt.Sprintf("the router refused (error %d)", e.Code)
}

type arg struct{ name, value string }

// call sends one SOAP action and returns the named values of the answer.
func (g *gateway) call(ctx context.Context, action string, args ...arg) (map[string]string, error) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	fmt.Fprintf(&body, `<u:%s xmlns:u="%s">`, action, g.service)
	for _, a := range args {
		body.WriteString("<" + a.name + ">")
		_ = xml.EscapeText(&body, []byte(a.value))
		body.WriteString("</" + a.name + ">")
	}
	fmt.Fprintf(&body, `</u:%s></s:Body></s:Envelope>`, action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.control, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+g.service+"#"+action+`"`)
	res, err := routerClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the router didn't answer: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	values := soapValues(b)
	if res.StatusCode != http.StatusOK {
		code, _ := strconv.Atoi(values["errorCode"])
		if code == 0 {
			return nil, fmt.Errorf("the router answered %s", res.Status)
		}
		return nil, &upnpError{Code: code, Description: values["errorDescription"]}
	}
	return values, nil
}

// soapValues collects the text of every leaf element in a SOAP answer.
func soapValues(b []byte) map[string]string {
	out := map[string]string{}
	d := xml.NewDecoder(bytes.NewReader(b))
	var name string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name = t.Name.Local
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if t.Name.Local == name {
				out[name] = strings.TrimSpace(text.String())
			}
			name = ""
		}
	}
}

// externalIP asks the router for its internet address.
func (g *gateway) externalIP(ctx context.Context) (netip.Addr, error) {
	v, err := g.call(ctx, "GetExternalIPAddress")
	if err != nil {
		return netip.Addr{}, err
	}
	a, err := netip.ParseAddr(v["NewExternalIPAddress"])
	if err != nil {
		return netip.Addr{}, errors.New("the router doesn't know its internet address yet")
	}
	return a, nil
}

// leaseSeconds is how long each forward lasts; they're renewed well before.
const leaseSeconds = 3600

// addMapping forwards a port on the router to this container. Routers that
// only take permanent forwards get one of those (removed when it closes).
func (g *gateway) addMapping(ctx context.Context, p Port, local netip.Addr) (permanent bool, err error) {
	add := func(lease int) error {
		_, err := g.call(ctx, "AddPortMapping",
			arg{"NewRemoteHost", ""},
			arg{"NewExternalPort", strconv.Itoa(p.Port)},
			arg{"NewProtocol", p.Proto},
			arg{"NewInternalPort", strconv.Itoa(p.Port)},
			arg{"NewInternalClient", local.String()},
			arg{"NewEnabled", "1"},
			arg{"NewPortMappingDescription", "Blockheads " + p.For},
			arg{"NewLeaseDuration", strconv.Itoa(lease)})
		return err
	}
	err = add(leaseSeconds)
	var ue *upnpError
	if errors.As(err, &ue) && ue.Code == 725 { // OnlyPermanentLeasesSupported
		return true, add(0)
	}
	return false, err
}

// mappedTo returns the device a forward on the router goes to ("" when the
// router has no such forward).
func (g *gateway) mappedTo(ctx context.Context, p Port) (string, error) {
	v, err := g.call(ctx, "GetSpecificPortMappingEntry",
		arg{"NewRemoteHost", ""},
		arg{"NewExternalPort", strconv.Itoa(p.Port)},
		arg{"NewProtocol", p.Proto})
	var ue *upnpError
	if errors.As(err, &ue) && (ue.Code == 714 || ue.Code == 713) { // no such forward
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v["NewInternalClient"], nil
}

func (g *gateway) deleteMapping(ctx context.Context, p Port) error {
	_, err := g.call(ctx, "DeletePortMapping",
		arg{"NewRemoteHost", ""},
		arg{"NewExternalPort", strconv.Itoa(p.Port)},
		arg{"NewProtocol", p.Proto})
	var ue *upnpError
	if errors.As(err, &ue) && ue.Code == 714 { // NoSuchEntryInArray: already gone
		return nil
	}
	return err
}
