/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
)

// Tunnel is the operator's plain-Go view of a Cloudflare tunnel.
type Tunnel struct {
	ID         string
	Name       string
	AccountTag string
}

// CreateTunnelParams is the input for CreateTunnel. config_src is fixed to
// "cloudflare" — local-config tunnels are not modeled.
type CreateTunnelParams struct {
	Name string
	// TunnelSecret, when set, supplies an explicit secret; otherwise
	// Cloudflare generates one. For remote-config tunnels we let
	// Cloudflare generate.
	TunnelSecret string
}

// PatchTunnelParams is the input for PatchTunnel. Only name is mutable on
// the operator surface; config_src is write-once and the connector secret
// is opaque to us.
type PatchTunnelParams struct {
	Name *string
}

// TunnelToken is the connector-join shared secret returned by
// GET /token. Treat as sensitive: never log it, never write it into status.
type TunnelToken string

// TunnelConfiguration is the response from GET / PUT /configurations.
type TunnelConfiguration struct {
	Version int
	Config  TunnelConfig
}

// TunnelConfig is the ingress-list payload sent to / received from the
// /configurations endpoint.
type TunnelConfig struct {
	Ingress       []IngressEntry        `json:"ingress"`
	OriginRequest *IngressOriginRequest `json:"originRequest,omitempty"`
}

// IngressEntry is one rule in the ingress list.
type IngressEntry struct {
	Hostname      string                `json:"hostname,omitempty"`
	Path          string                `json:"path,omitempty"`
	Service       string                `json:"service"`
	OriginRequest *IngressOriginRequest `json:"originRequest,omitempty"`
}

// IngressOriginRequest is the per-entry origin-request override. Only the
// fields the operator currently models are included; the SDK surface
// carries many more that we do not propagate.
type IngressOriginRequest struct {
	NoTLSVerify           *bool   `json:"noTLSVerify,omitempty"`
	OriginServerName      *string `json:"originServerName,omitempty"`
	CAPool                *string `json:"caPool,omitempty"`
	ConnectTimeoutSeconds *int32  `json:"connectTimeout,omitempty"`
}

// TunnelConnection is one connector connection summary. ColoName and
// IsPendingReconnect are flattened from the first underlying ClientConn
// when the SDK reports any.
type TunnelConnection struct {
	ID                 string
	ColoName           string
	IsPendingReconnect bool
}

// ErrTunnelNotFound is returned (wrapped) when the Cloudflare API responds
// with 404 to a per-tunnel-ID operation. Callers can branch via
// errors.Is(err, ErrTunnelNotFound) — used by the finalizer drain so a
// missing tunnel does not block deletion.
var ErrTunnelNotFound = errors.New("tunnel not found")

// classifyTunnelAPIErr maps cloudflare-go errors to ErrTunnelNotFound when
// the underlying *cfgo.Error has StatusCode 404. Non-404 errors pass
// through unchanged. Mirrors classifyDNSAPIErr / classifyZoneAPIErr.
func classifyTunnelAPIErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *cfgo.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %w", ErrTunnelNotFound, err)
	}
	return err
}

// tunnelClient is the production SDK wrapper.
type tunnelClient struct {
	cf *cfgo.Client
}

// NewTunnelClientFromCF builds a TunnelClient from a cloudflare-go client.
func NewTunnelClientFromCF(cf *cfgo.Client) TunnelClient {
	return &tunnelClient{cf: cf}
}

func (c *tunnelClient) GetTunnel(ctx context.Context, accountID, tunnelID string) (*Tunnel, error) {
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredGetParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("get tunnel %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	return &Tunnel{ID: resp.ID, Name: resp.Name, AccountTag: resp.AccountTag}, nil
}

func (c *tunnelClient) ListTunnelsByName(ctx context.Context, accountID, name string) ([]Tunnel, error) {
	page, err := c.cf.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cfgo.F(accountID),
		Name:      cfgo.F(name),
		IsDeleted: cfgo.F(false),
	})
	if err != nil {
		return nil, fmt.Errorf("list tunnels by name=%q: %w", name, err)
	}
	out := make([]Tunnel, 0, len(page.Result))
	for _, t := range page.Result {
		out = append(out, Tunnel{ID: t.ID, Name: t.Name, AccountTag: t.AccountTag})
	}
	return out, nil
}

func (c *tunnelClient) CreateTunnel(ctx context.Context, accountID string, params CreateTunnelParams) (*Tunnel, error) {
	body := zero_trust.TunnelCloudflaredNewParams{
		AccountID: cfgo.F(accountID),
		Name:      cfgo.F(params.Name),
		ConfigSrc: cfgo.F(zero_trust.TunnelCloudflaredNewParamsConfigSrcCloudflare),
	}
	if params.TunnelSecret != "" {
		body.TunnelSecret = cfgo.F(params.TunnelSecret)
	}
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.New(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("create tunnel name=%q: %w", params.Name, err)
	}
	return &Tunnel{ID: resp.ID, Name: resp.Name, AccountTag: resp.AccountTag}, nil
}

func (c *tunnelClient) PatchTunnel(ctx context.Context, accountID, tunnelID string, params PatchTunnelParams) (*Tunnel, error) {
	body := zero_trust.TunnelCloudflaredEditParams{
		AccountID: cfgo.F(accountID),
	}
	if params.Name != nil {
		body.Name = cfgo.F(*params.Name)
	}
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Edit(ctx, tunnelID, body)
	if err != nil {
		return nil, fmt.Errorf("patch tunnel %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	return &Tunnel{ID: resp.ID, Name: resp.Name, AccountTag: resp.AccountTag}, nil
}

func (c *tunnelClient) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	_, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredDeleteParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return fmt.Errorf("delete tunnel %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	return nil
}

func (c *tunnelClient) GetConfiguration(ctx context.Context, accountID, tunnelID string) (*TunnelConfiguration, error) {
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Configurations.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredConfigurationGetParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("get configuration %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	return mapConfigurationGetResponse(resp), nil
}

func (c *tunnelClient) PutConfiguration(ctx context.Context, accountID, tunnelID string, cfg TunnelConfig) (*TunnelConfiguration, error) {
	body := zero_trust.TunnelCloudflaredConfigurationUpdateParams{
		AccountID: cfgo.F(accountID),
		Config:    cfgo.F(toSDKConfig(cfg)),
	}
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Configurations.Update(ctx, tunnelID, body)
	if err != nil {
		return nil, fmt.Errorf("put configuration %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	return mapConfigurationUpdateResponse(resp), nil
}

func (c *tunnelClient) GetToken(ctx context.Context, accountID, tunnelID string) (TunnelToken, error) {
	// Token.Get returns *string (the raw connector token), not a wrapper
	// struct. Error message intentionally elides the token value.
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Token.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredTokenGetParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return "", fmt.Errorf("get token %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	if resp == nil {
		return "", nil
	}
	return TunnelToken(*resp), nil
}

func (c *tunnelClient) ListConnections(ctx context.Context, accountID, tunnelID string) ([]TunnelConnection, error) {
	// SDK quirk: the connections list endpoint is exposed as Connections.Get
	// returning *pagination.SinglePage[Client]. There is no .List on this
	// sub-service. Param type is TunnelCloudflaredConnectionGetParams.
	page, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Connections.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredConnectionGetParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("list connections %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	out := make([]TunnelConnection, 0, len(page.Result))
	for _, cli := range page.Result {
		conn := TunnelConnection{ID: cli.ID}
		// Flattens the SDK's per-connector connection list to a single
		// representative ColoName/IsPendingReconnect pair (taken from the
		// first underlying ClientConn). Callers needing the full list
		// should iterate the SDK response directly.
		if len(cli.Conns) > 0 {
			conn.ColoName = cli.Conns[0].ColoName
			conn.IsPendingReconnect = cli.Conns[0].IsPendingReconnect
		}
		out = append(out, conn)
	}
	return out, nil
}

func (c *tunnelClient) DeleteConnections(ctx context.Context, accountID, tunnelID string) error {
	_, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Connections.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredConnectionDeleteParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return fmt.Errorf("delete connections %s: %w", tunnelID, classifyTunnelAPIErr(err))
	}
	return nil
}

// --- mapping helpers (SDK <-> plain Go) ---

// mapConfigurationGetResponse maps the GET /configurations response.
//
// Projects all four operator-modeled OriginRequest fields symmetrically
// with toSDKConfig's write path. The SDK uses plain (non-pointer) bool/
// string/int64 fields, so we can't distinguish "explicitly false" from
// "unset" on bool fields — NoTLSVerify is projected only when true (the
// unset-vs-explicit-false ambiguity is unavoidable). String and numeric
// fields are projected when non-zero. If any field is set, OriginRequest
// is attached to the entry; otherwise it stays nil.
func mapConfigurationGetResponse(resp *zero_trust.TunnelCloudflaredConfigurationGetResponse) *TunnelConfiguration {
	out := &TunnelConfiguration{Version: int(resp.Version)}
	for _, in := range resp.Config.Ingress {
		entry := IngressEntry{Hostname: in.Hostname, Path: in.Path, Service: in.Service}
		or := IngressOriginRequest{}
		hasAny := false
		if in.OriginRequest.NoTLSVerify {
			b := true
			or.NoTLSVerify = &b
			hasAny = true
		}
		if s := in.OriginRequest.OriginServerName; s != "" {
			or.OriginServerName = &s
			hasAny = true
		}
		if s := in.OriginRequest.CAPool; s != "" {
			or.CAPool = &s
			hasAny = true
		}
		if ct := in.OriginRequest.ConnectTimeout; ct != 0 {
			v := int32(ct)
			or.ConnectTimeoutSeconds = &v
			hasAny = true
		}
		if hasAny {
			entry.OriginRequest = &or
		}
		out.Config.Ingress = append(out.Config.Ingress, entry)
	}
	return out
}

// mapConfigurationUpdateResponse maps the PUT /configurations response.
// The Update response type is structurally identical to the Get response
// for the fields we project, but the SDK exposes them as a distinct named
// type; keep a dedicated mapper so we do not couple the two. Projection
// rules match mapConfigurationGetResponse exactly — see that doc for
// detail. The two mappers are kept symmetric so drift detection (T9)
// reads back what PUT wrote.
func mapConfigurationUpdateResponse(resp *zero_trust.TunnelCloudflaredConfigurationUpdateResponse) *TunnelConfiguration {
	out := &TunnelConfiguration{Version: int(resp.Version)}
	for _, in := range resp.Config.Ingress {
		entry := IngressEntry{Hostname: in.Hostname, Path: in.Path, Service: in.Service}
		or := IngressOriginRequest{}
		hasAny := false
		if in.OriginRequest.NoTLSVerify {
			b := true
			or.NoTLSVerify = &b
			hasAny = true
		}
		if s := in.OriginRequest.OriginServerName; s != "" {
			or.OriginServerName = &s
			hasAny = true
		}
		if s := in.OriginRequest.CAPool; s != "" {
			or.CAPool = &s
			hasAny = true
		}
		if ct := in.OriginRequest.ConnectTimeout; ct != 0 {
			v := int32(ct)
			or.ConnectTimeoutSeconds = &v
			hasAny = true
		}
		if hasAny {
			entry.OriginRequest = &or
		}
		out.Config.Ingress = append(out.Config.Ingress, entry)
	}
	return out
}

// toSDKConfig converts the plain-Go TunnelConfig into the SDK Update
// payload. The SDK uses param.Field[T] for every field — caller must wrap
// with cfgo.F when the value is meaningful.
func toSDKConfig(cfg TunnelConfig) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig {
	ingress := make([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, 0, len(cfg.Ingress))
	for _, e := range cfg.Ingress {
		ie := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
			Hostname: cfgo.F(e.Hostname),
			Path:     cfgo.F(e.Path),
			Service:  cfgo.F(e.Service),
		}
		if e.OriginRequest != nil {
			or := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
			if e.OriginRequest.NoTLSVerify != nil {
				or.NoTLSVerify = cfgo.F(*e.OriginRequest.NoTLSVerify)
			}
			if e.OriginRequest.OriginServerName != nil {
				or.OriginServerName = cfgo.F(*e.OriginRequest.OriginServerName)
			}
			if e.OriginRequest.CAPool != nil {
				or.CAPool = cfgo.F(*e.OriginRequest.CAPool)
			}
			if e.OriginRequest.ConnectTimeoutSeconds != nil {
				or.ConnectTimeout = cfgo.F(int64(*e.OriginRequest.ConnectTimeoutSeconds))
			}
			ie.OriginRequest = cfgo.F(or)
		}
		ingress = append(ingress, ie)
	}
	return zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfig{
		Ingress: cfgo.F(ingress),
	}
}
