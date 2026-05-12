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
	"github.com/cloudflare/cloudflare-go/v6/dns"
)

// ErrRecordNotFound is returned when the Cloudflare API responds with 404
// to a DNS record lookup. Use errors.Is to match.
var ErrRecordNotFound = errors.New("dns record not found")

// classifyDNSAPIErr maps cloudflare-go errors to ErrRecordNotFound when
// the API responds with 404 on a record path. Other errors pass through.
func classifyDNSAPIErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *cfgo.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %w", ErrRecordNotFound, err)
	}
	return err
}

// dnsClient wraps the cloudflare-go v6 SDK to implement DNSClient.
type dnsClient struct {
	cf *cfgo.Client
}

// NewDNSClientFromCF creates a DNSClient from a cloudflare-go Client.
func NewDNSClientFromCF(cf *cfgo.Client) DNSClient {
	return &dnsClient{cf: cf}
}

func (c *dnsClient) GetRecord(ctx context.Context, zoneID, recordID string) (*DNSRecord, error) {
	resp, err := c.cf.DNS.Records.Get(ctx, recordID, dns.RecordGetParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return nil, fmt.Errorf("get DNS record %s: %w", recordID, classifyDNSAPIErr(err))
	}
	return mapRecordResponse(resp), nil
}

func (c *dnsClient) ListRecordsByNameAndType(ctx context.Context, zoneID, name, recordType string) ([]DNSRecord, error) {
	page, err := c.cf.DNS.Records.List(ctx, dns.RecordListParams{
		ZoneID: cfgo.F(zoneID),
		Name:   cfgo.F(dns.RecordListParamsName{Exact: cfgo.F(name)}),
		Type:   cfgo.F(dns.RecordListParamsType(recordType)),
	})
	if err != nil {
		return nil, fmt.Errorf("list DNS records: %w", err)
	}

	records := make([]DNSRecord, 0, len(page.Result))
	for _, r := range page.Result {
		records = append(records, *mapRecordResponse(&r))
	}
	return records, nil
}

func (c *dnsClient) CreateRecord(ctx context.Context, zoneID string, params DNSRecordParams) (*DNSRecord, error) {
	body := dns.RecordNewParamsBody{
		Name:    cfgo.F(params.Name),
		Type:    cfgo.F(dns.RecordNewParamsBodyType(params.Type)),
		Content: cfgo.F(params.Content),
		TTL:     cfgo.F(dns.TTL(params.TTL)),
	}
	if params.Proxied != nil {
		body.Proxied = cfgo.F(*params.Proxied)
	}
	if params.Priority != nil {
		body.Priority = cfgo.F(float64(*params.Priority))
	}
	if params.Data != nil {
		body.Data = cfgo.F[any](params.Data)
	}

	resp, err := c.cf.DNS.Records.New(ctx, dns.RecordNewParams{
		ZoneID: cfgo.F(zoneID),
		Body:   body,
	})
	if err != nil {
		return nil, fmt.Errorf("create DNS record: %w", err)
	}
	return mapRecordResponse(resp), nil
}

func (c *dnsClient) UpdateRecord(ctx context.Context, zoneID, recordID string, params DNSRecordParams) (*DNSRecord, error) {
	body := dns.RecordUpdateParamsBody{
		Name:    cfgo.F(params.Name),
		Type:    cfgo.F(dns.RecordUpdateParamsBodyType(params.Type)),
		Content: cfgo.F(params.Content),
		TTL:     cfgo.F(dns.TTL(params.TTL)),
	}
	if params.Proxied != nil {
		body.Proxied = cfgo.F(*params.Proxied)
	}
	if params.Priority != nil {
		body.Priority = cfgo.F(float64(*params.Priority))
	}
	if params.Data != nil {
		body.Data = cfgo.F[any](params.Data)
	}

	resp, err := c.cf.DNS.Records.Update(ctx, recordID, dns.RecordUpdateParams{
		ZoneID: cfgo.F(zoneID),
		Body:   body,
	})
	if err != nil {
		return nil, fmt.Errorf("update DNS record %s: %w", recordID, classifyDNSAPIErr(err))
	}
	return mapRecordResponse(resp), nil
}

func (c *dnsClient) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	_, err := c.cf.DNS.Records.Delete(ctx, recordID, dns.RecordDeleteParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("delete DNS record %s: %w", recordID, classifyDNSAPIErr(err))
	}
	return nil
}

// mapRecordResponse converts a Cloudflare SDK RecordResponse to our internal
// DNSRecord. The Priority field on DNSRecord is populated only when the SDK
// surfaces a non-zero Priority (MX / SRV / URI records). For other record
// types Priority is left nil.
func mapRecordResponse(r *dns.RecordResponse) *DNSRecord {
	rec := &DNSRecord{
		ID:      r.ID,
		Name:    r.Name,
		Type:    string(r.Type),
		Content: r.Content,
		Proxied: r.Proxied,
		TTL:     int(r.TTL),
	}
	if r.Priority != 0 {
		p := int(r.Priority)
		rec.Priority = &p
	}
	if m, ok := r.Data.(map[string]any); ok {
		rec.Data = m
	}
	return rec
}
