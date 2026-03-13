package cloudflare

import (
	"context"
	"fmt"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
)

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
		return nil, fmt.Errorf("get DNS record %s: %w", recordID, err)
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
		return nil, fmt.Errorf("update DNS record %s: %w", recordID, err)
	}
	return mapRecordResponse(resp), nil
}

func (c *dnsClient) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	_, err := c.cf.DNS.Records.Delete(ctx, recordID, dns.RecordDeleteParams{
		ZoneID: cfgo.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("delete DNS record %s: %w", recordID, err)
	}
	return nil
}

// mapRecordResponse converts a Cloudflare SDK RecordResponse to our internal DNSRecord.
func mapRecordResponse(r *dns.RecordResponse) *DNSRecord {
	rec := &DNSRecord{
		ID:      r.ID,
		Name:    r.Name,
		Type:    string(r.Type),
		Content: r.Content,
		Proxied: r.Proxied,
		TTL:     int(r.TTL),
	}
	// Map the Data field if it's a map
	if m, ok := r.Data.(map[string]any); ok {
		rec.Data = m
	}
	return rec
}
