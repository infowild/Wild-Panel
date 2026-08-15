package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/util/netsafe"
)

// panelEnvelope is the standard {success,msg,obj} response every panel API returns.
type panelEnvelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// RemoteInbound is the subset of a remote inbound list row we need for reconcile
// and traffic pull.
type RemoteInbound struct {
	Id          int             `json:"id"`
	Remark      string          `json:"remark"`
	Enable      bool            `json:"enable"`
	Port        int             `json:"port"`
	Protocol    string          `json:"protocol"`
	Tag         string          `json:"tag"`
	Up          int64           `json:"up"`
	Down        int64           `json:"down"`
	Total       int64           `json:"total"`
	Settings    string          `json:"settings"`
	ClientStats json.RawMessage `json:"clientStats"`
}

// RemoteClientStat is one clientStats entry from a remote inbound list.
type RemoteClientStat struct {
	Email   string `json:"email"`
	Up      int64  `json:"up"`
	Down    int64  `json:"down"`
	AllTime int64  `json:"allTime"`
	Enable  bool   `json:"enable"`
}

// RemoteStatus is the subset of /server/status used by heartbeat.
type RemoteStatus struct {
	Cpu    float64 `json:"cpu"`
	Uptime uint64  `json:"uptime"`
	Mem    struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"mem"`
	Xray struct {
		State    string `json:"state"`
		ErrorMsg string `json:"errorMsg"`
		Version  string `json:"version"`
	} `json:"xray"`
	NetIO struct {
		Up   uint64 `json:"up"`
		Down uint64 `json:"down"`
	} `json:"netIO"`
	PanelVersion string `json:"panelVersion"`
	PanelGuid    string `json:"panelGuid"`
}

// RemoteNode talks to a remote panel's /panel/api surface with a Bearer token.
type RemoteNode struct {
	node   *model.Node
	client *http.Client
	token  string
}

// NewRemoteNode builds an HTTP client for n. token is the plaintext Bearer secret.
func NewRemoteNode(n *model.Node, token string) (*RemoteNode, error) {
	if n == nil {
		return nil, common.NewError("node is required")
	}
	if strings.TrimSpace(token) == "" {
		return nil, common.NewError("api token is required")
	}
	scheme := strings.ToLower(strings.TrimSpace(n.Scheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	n.Scheme = scheme

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch strings.ToLower(strings.TrimSpace(n.TlsVerifyMode)) {
	case "skip":
		tlsCfg.InsecureSkipVerify = true
	case "pin":
		tlsCfg.InsecureSkipVerify = true
		pin := normalizePin(n.PinnedCertSha256)
		if pin == "" {
			return nil, common.NewError("pinned certificate fingerprint is required for pin mode")
		}
		wantHex := pin
		wantB64 := strings.TrimSpace(n.PinnedCertSha256)
		tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("no peer certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			gotHex := strings.ToLower(hex.EncodeToString(sum[:]))
			gotB64 := base64.StdEncoding.EncodeToString(sum[:])
			if gotHex == wantHex || strings.EqualFold(gotB64, wantB64) {
				return nil
			}
			return fmt.Errorf("certificate pin mismatch")
		}
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx = netsafe.ContextWithAllowPrivate(ctx, n.AllowPrivateAddress)
			return netsafe.DialContext(ctx, network, address)
		},
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	return &RemoteNode{
		node:   n,
		token:  strings.TrimSpace(token),
		client: &http.Client{Timeout: 25 * time.Second, Transport: transport},
	}, nil
}

func normalizePin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Accept hex (with or without colons) or standard base64.
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == sha256.Size {
		return strings.ToLower(hex.EncodeToString(b))
	}
	cleaned := strings.ReplaceAll(strings.ToLower(raw), ":", "")
	if _, err := hex.DecodeString(cleaned); err == nil && len(cleaned) == sha256.Size*2 {
		return cleaned
	}
	return strings.ToLower(raw)
}

func (r *RemoteNode) apiURL(path string) string {
	base := strings.TrimSpace(r.node.BasePath)
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	path = strings.TrimPrefix(path, "/")
	return fmt.Sprintf("%s://%s:%d%spanel/api/%s",
		r.node.Scheme, r.node.Address, r.node.Port, base, path)
}

func (r *RemoteNode) do(method, path string, body io.Reader, contentType string) (*panelEnvelope, error) {
	req, err := http.NewRequest(method, r.apiURL(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, common.NewError("remote API not found (check base path / token)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("remote HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var env panelEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("remote response not JSON: %w", err)
	}
	if !env.Success {
		msg := env.Msg
		if msg == "" {
			msg = "remote request failed"
		}
		return &env, common.NewError(msg)
	}
	return &env, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Status calls GET /panel/api/server/status.
func (r *RemoteNode) Status() (*RemoteStatus, time.Duration, error) {
	start := time.Now()
	env, err := r.do(http.MethodGet, "server/status", nil, "")
	latency := time.Since(start)
	if err != nil {
		return nil, latency, err
	}
	var st RemoteStatus
	if len(env.Obj) > 0 {
		if err := json.Unmarshal(env.Obj, &st); err != nil {
			return nil, latency, err
		}
	}
	return &st, latency, nil
}

// ListInbounds calls GET /panel/api/inbounds/list.
func (r *RemoteNode) ListInbounds() ([]RemoteInbound, error) {
	env, err := r.do(http.MethodGet, "inbounds/list", nil, "")
	if err != nil {
		return nil, err
	}
	var rows []RemoteInbound
	if len(env.Obj) > 0 {
		if err := json.Unmarshal(env.Obj, &rows); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// AddInbound POSTs a form-urlencoded inbound create.
func (r *RemoteNode) AddInbound(form url.Values) (*RemoteInbound, error) {
	env, err := r.do(http.MethodPost, "inbounds/add", strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded; charset=UTF-8")
	if err != nil {
		return nil, err
	}
	var row RemoteInbound
	if len(env.Obj) > 0 {
		_ = json.Unmarshal(env.Obj, &row)
	}
	return &row, nil
}

// UpdateInbound POSTs a form-urlencoded inbound update.
func (r *RemoteNode) UpdateInbound(id int, form url.Values) error {
	_, err := r.do(http.MethodPost, "inbounds/update/"+strconv.Itoa(id),
		strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded; charset=UTF-8")
	return err
}

// DelInbound deletes a remote inbound by id.
func (r *RemoteNode) DelInbound(id int) error {
	_, err := r.do(http.MethodPost, "inbounds/del/"+strconv.Itoa(id), nil, "")
	return err
}

// FetchCertFingerprint dials the node TLS endpoint insecurely and returns the
// leaf certificate's SHA-256 as standard base64 (3x-ui convention).
func FetchCertFingerprint(n *model.Node) (string, error) {
	if n == nil {
		return "", common.NewError("node is required")
	}
	scheme := strings.ToLower(strings.TrimSpace(n.Scheme))
	if scheme != "https" {
		return "", common.NewError("certificate pin requires https")
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx = netsafe.ContextWithAllowPrivate(ctx, n.AllowPrivateAddress)
			return netsafe.DialContext(ctx, network, address)
		},
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}
	base := strings.TrimSpace(n.BasePath)
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	u := fmt.Sprintf("https://%s:%d%s", n.Address, n.Port, base)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		// Handshake may still have completed; try to recover from URL error wrappers.
		return "", err
	}
	defer resp.Body.Close()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return "", common.NewError("no TLS certificate presented")
	}
	sum := sha256.Sum256(resp.TLS.PeerCertificates[0].Raw)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// WireInboundForm builds the form-urlencoded payload a remote add/update expects.
func WireInboundForm(in *model.Inbound) url.Values {
	v := url.Values{}
	if in == nil {
		return v
	}
	v.Set("remark", in.Remark)
	v.Set("enable", strconv.FormatBool(in.Enable))
	v.Set("expiryTime", strconv.FormatInt(in.ExpiryTime, 10))
	v.Set("listen", in.Listen)
	v.Set("port", strconv.Itoa(in.Port))
	v.Set("protocol", string(in.Protocol))
	v.Set("settings", in.Settings)
	v.Set("streamSettings", in.StreamSettings)
	v.Set("tag", in.Tag)
	v.Set("sniffing", in.Sniffing)
	v.Set("total", strconv.FormatInt(in.Total, 10))
	v.Set("trafficReset", in.TrafficReset)
	v.Set("up", "0")
	v.Set("down", "0")
	// Wild Panel extras — stock 3x-ui ignores unknown form keys.
	v.Set("trafficMultiplierEnable", strconv.FormatBool(in.TrafficMultiplierEnable))
	v.Set("trafficMultiplierAfter", strconv.FormatInt(in.TrafficMultiplierAfter, 10))
	v.Set("trafficMultiplier", strconv.FormatFloat(in.TrafficMultiplier, 'f', -1, 64))
	v.Set("speedLimitEnable", strconv.FormatBool(in.SpeedLimitEnable))
	v.Set("speedLimitSeparate", strconv.FormatBool(in.SpeedLimitSeparate))
	v.Set("speedLimitDown", strconv.Itoa(in.SpeedLimitDown))
	v.Set("speedLimitUp", strconv.Itoa(in.SpeedLimitUp))
	v.Set("speedLimitAfter", strconv.FormatInt(in.SpeedLimitAfter, 10))
	v.Set("ipLimit", strconv.Itoa(in.IPLimit))
	v.Set("ipLimitStrategy", in.IPLimitStrategy)
	return v
}

// FormFingerprint is a stable SHA-256 of the wire form used to skip no-op pushes.
func FormFingerprint(form url.Values) string {
	sum := sha256.Sum256([]byte(form.Encode()))
	return hex.EncodeToString(sum[:])
}

// ParseRemoteClientStats extracts client traffic rows from a remote inbound.
func ParseRemoteClientStats(raw json.RawMessage) []RemoteClientStat {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var rows []RemoteClientStat
	if err := json.Unmarshal(raw, &rows); err == nil {
		return rows
	}
	return nil
}
