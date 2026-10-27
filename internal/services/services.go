// Package services ports tuta_alias/services.py — high-level operations on
// domains, aliases, and quota.
package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// ListDomains returns the custom domains on the account by loading
// CustomerInfo and extracting the domainInfos list.
func ListDomains(c *api.Client, s *session.Session) ([]string, error) {
	if len(s.CustomerInfoID) != 2 {
		return []string{}, nil
	}
	info, err := c.EntityLoad(consts.SysApp, "CustomerInfo", "CustomerInfo",
		s.CustomerInfoID[0], s.CustomerInfoID[1])
	if err != nil {
		return nil, err
	}
	out := []string{}
	if dis, ok := info["domainInfos"].([]map[string]any); ok {
		for _, di := range dis {
			if d, _ := di["domain"].(string); d != "" {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

// AddressEntry holds a single email address with its state flags.
type AddressEntry struct {
	MailAddress string `json:"mailAddress"`
	Enabled     bool   `json:"enabled"`
	Primary     bool   `json:"primary"` // explicit false matches Python output
}

// ListAddresses returns the primary address and all aliases from GroupInfo.
func ListAddresses(c *api.Client, s *session.Session) (primary string, aliases []AddressEntry, err error) {
	if len(s.GroupInfoID) != 2 {
		return "", nil, fmt.Errorf("session missing group_info_id")
	}
	gi, err := c.EntityLoad(consts.SysApp, "GroupInfo", "GroupInfo",
		s.GroupInfoID[0], s.GroupInfoID[1])
	if err != nil {
		return "", nil, err
	}
	primary, _ = gi["mailAddress"].(string)
	if as, ok := gi["mailAddressAliases"].([]map[string]any); ok {
		for _, a := range as {
			entry := AddressEntry{}
			entry.MailAddress, _ = a["mailAddress"].(string)
			if b, ok := a["enabled"].(bool); ok {
				entry.Enabled = b
			} else {
				entry.Enabled = true
			}
			aliases = append(aliases, entry)
		}
	}
	return primary, aliases, nil
}

// AliasesByDomain groups all addresses (primary + aliases) by domain suffix.
func AliasesByDomain(c *api.Client, s *session.Session) (map[string][]AddressEntry, error) {
	primary, aliases, err := ListAddresses(c, s)
	if err != nil {
		return nil, err
	}
	out := map[string][]AddressEntry{}
	if primary != "" {
		if i := strings.LastIndex(primary, "@"); i > 0 {
			d := primary[i+1:]
			out[d] = append(out[d], AddressEntry{MailAddress: primary, Enabled: true, Primary: true})
		}
	}
	for _, a := range aliases {
		if a.MailAddress == "" {
			continue
		}
		i := strings.LastIndex(a.MailAddress, "@")
		if i <= 0 {
			continue
		}
		d := a.MailAddress[i+1:]
		out[d] = append(out[d], AddressEntry{
			MailAddress: a.MailAddress, Enabled: a.Enabled, Primary: false,
		})
	}
	return out, nil
}

// AddAlias creates a new alias on the account. The group field must be
// UserGroupID per the upstream protocol — NOT MailGroupID.
func AddAlias(c *api.Client, s *session.Session, mailAddress string) error {
	_, err := c.ServiceRequest("POST", consts.SysApp, "MailAddressAliasService",
		"MailAddressAliasServiceData", map[string]any{
			"_format":     0,
			"mailAddress": strings.ToLower(strings.TrimSpace(mailAddress)),
			"group":       s.UserGroupID, // upstream contract — user group, NOT mail group
		}, "")
	return err
}

// RemoveAlias disables (or re-enables when restore=true) an alias.
func RemoveAlias(c *api.Client, s *session.Session, mailAddress string, restore bool) error {
	_, err := c.ServiceRequest("DELETE", consts.SysApp, "MailAddressAliasService",
		"MailAddressAliasServiceDataDelete", map[string]any{
			"_format":     0,
			"mailAddress": strings.ToLower(strings.TrimSpace(mailAddress)),
			"restore":     restore,
			"group":       s.UserGroupID,
		}, "")
	return err
}

// Quota holds alias quota counters returned by the service.
type Quota struct {
	NbrOfFreeAliases int `json:"nbrOfFreeAliases"`
	TotalAliases     int `json:"totalAliases"`
	UsedAliases      int `json:"usedAliases"`
	EnabledAliases   int `json:"enabledAliases"`
}

// Remaining is how many more aliases fit. The web client never reads
// NbrOfFreeAliases; it treats used >= total as the limit (AddAliasDialog.ts).
func (q *Quota) Remaining() int {
	return max(q.TotalAliases-q.UsedAliases, 0)
}

// AliasQuota fetches alias quota for the account.
func AliasQuota(c *api.Client, s *session.Session) (*Quota, error) {
	out, err := c.ServiceRequest("GET", consts.SysApp, "MailAddressAliasService",
		"MailAddressAliasGetIn", map[string]any{
			"_format":     0,
			"targetGroup": s.UserGroupID,
		}, "MailAddressAliasServiceReturn")
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	q := &Quota{}
	if v, ok := m["nbrOfFreeAliases"].(int); ok {
		q.NbrOfFreeAliases = v
	}
	if v, ok := m["totalAliases"].(int); ok {
		q.TotalAliases = v
	}
	if v, ok := m["usedAliases"].(int); ok {
		q.UsedAliases = v
	}
	if v, ok := m["enabledAliases"].(int); ok {
		q.EnabledAliases = v
	}
	return q, nil
}

var hintRE = regexp.MustCompile(`^[a-zA-Z0-9-]{1,32}$`)

// SuggestAlias generates a random alias in the form hint-XXXXXXXX@domain
// where XXXXXXXX is 8 random hex characters (4 bytes).
func SuggestAlias(hint, domain string) (string, error) {
	if hint == "" || !hintRE.MatchString(hint) {
		hint = "tuta"
	}
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s@%s", hint, hex.EncodeToString(buf), domain), nil
}

// DNS record types and check results, mirroring DnsRecordType /
// CustomDomainCheckResult in src/platform-kit/app-env/TutanotaConstants.ts.
var DNSRecordTypeNames = map[int]string{
	0: "MX", 1: "SPF", 2: "DKIM", 3: "DMARC", 4: "MTA_STS", 5: "VERIFY",
}

var DNSCheckResultNames = map[int]string{
	0: "OK", 1: "DNS_LOOKUP_FAILED", 2: "DOMAIN_NOT_FOUND", 3: "NAMESERVER_NOT_FOUND",
}

// DNSRecord is one record as Tuta wants it. Subdomain is empty for the apex.
type DNSRecord struct {
	Type      string `json:"type"`
	Subdomain string `json:"subdomain"`
	Value     string `json:"value"`
}

// DNSCheck is the result of CustomDomainCheckService — the same data the
// web client's "refresh" button in the custom-domain wizard shows.
type DNSCheck struct {
	Domain          string      `json:"domain"`
	CheckResult     string      `json:"checkResult"`
	RequiredRecords []DNSRecord `json:"requiredRecords"`
	MissingRecords  []DNSRecord `json:"missingRecords"`
	InvalidRecords  []DNSRecord `json:"invalidRecords"`
}

// CheckDomainDNS asks Tuta to look up the domain's DNS records and compare
// them with what it requires (sys/CustomDomainCheckService GET, see
// src/applications/mail-app/settings/DomainDnsStatus.ts).
func CheckDomainDNS(c *api.Client, domain string) (*DNSCheck, error) {
	out, err := c.ServiceRequest("GET", consts.SysApp, "CustomDomainCheckService",
		"CustomDomainCheckGetIn", map[string]any{
			"_format": 0,
			"domain":  domain,
		}, "CustomDomainCheckGetOut")
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	res := &DNSCheck{Domain: domain}
	cr, _ := m["checkResult"].(int)
	if name, ok := DNSCheckResultNames[cr]; ok {
		res.CheckResult = name
	} else {
		res.CheckResult = fmt.Sprintf("UNKNOWN_%d", cr)
	}
	res.RequiredRecords = dnsRecords(m["requiredRecords"])
	res.MissingRecords = dnsRecords(m["missingRecords"])
	res.InvalidRecords = dnsRecords(m["invalidRecords"])
	return res, nil
}

func dnsRecords(raw any) []DNSRecord {
	out := []DNSRecord{}
	list, _ := raw.([]map[string]any)
	for _, r := range list {
		t, _ := r["type"].(int)
		name, ok := DNSRecordTypeNames[t]
		if !ok {
			name = fmt.Sprintf("UNKNOWN_%d", t)
		}
		sub, _ := r["subdomain"].(string)
		val, _ := r["value"].(string)
		out = append(out, DNSRecord{Type: name, Subdomain: sub, Value: val})
	}
	return out
}

// DomainValidationRecord returns the ownership TXT value Tuta expects at the
// apex of domain ("t-verify=<hex>"). The web client computes it locally —
// CustomerFacade.getDomainValidationRecord: first 16 bytes of
// sha256(lower(trim(domain)) + customerId), hex-encoded.
func DomainValidationRecord(domain, customerID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(domain)) + customerID))
	return "t-verify=" + hex.EncodeToString(sum[:16])
}
