package cliflow

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/services"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// ReadOpts carries optional flags shared by read commands.
type ReadOpts struct {
	AsJSON       bool
	DomainFilter string
}

// Whoami prints the email address of the currently logged-in account.
func Whoami(stdout, stderr io.Writer) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	fmt.Fprintln(stdout, s.MailAddress)
	return 0
}

// Domains lists the custom domains on the account.
func Domains(stdout, stderr io.Writer, opts ReadOpts) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	c := api.NewClient(consts.BaseURL, s.AccessToken)
	doms, err := services.ListDomains(c, s)
	if err != nil {
		return bail(stderr, err)
	}
	if opts.AsJSON {
		b, _ := json.MarshalIndent(doms, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	if len(doms) == 0 {
		fmt.Fprintln(stdout, "(no custom domains)")
		return 0
	}
	for _, d := range doms {
		fmt.Fprintln(stdout, d)
	}
	return 0
}

// Aliases lists email addresses grouped by domain.
func Aliases(stdout, stderr io.Writer, opts ReadOpts) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	c := api.NewClient(consts.BaseURL, s.AccessToken)
	grouped, err := services.AliasesByDomain(c, s)
	if err != nil {
		return bail(stderr, err)
	}
	if opts.DomainFilter != "" {
		grouped = map[string][]services.AddressEntry{opts.DomainFilter: grouped[opts.DomainFilter]}
	}
	if opts.AsJSON {
		b, _ := json.MarshalIndent(grouped, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	if len(grouped) == 0 {
		fmt.Fprintln(stdout, "(no addresses)")
		return 0
	}
	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, d := range keys {
		fmt.Fprintf(stdout, "%s:\n", d)
		for _, item := range grouped[d] {
			tag := ""
			if item.Primary {
				tag = " (primary)"
			}
			state := ""
			if !item.Enabled {
				state = " [disabled]"
			}
			fmt.Fprintf(stdout, "  - %s%s%s\n", item.MailAddress, tag, state)
		}
	}
	return 0
}

// QuotaCmd prints alias quota information.
func QuotaCmd(stdout, stderr io.Writer, asJSON bool) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	c := api.NewClient(consts.BaseURL, s.AccessToken)
	q, err := services.AliasQuota(c, s)
	if err != nil {
		return bail(stderr, err)
	}
	if asJSON {
		b, _ := json.MarshalIndent(q, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	fmt.Fprintf(stdout, "used %d/%d (enabled %d, %d left)\n",
		q.UsedAliases, q.TotalAliases, q.EnabledAliases, q.Remaining())
	return 0
}

// DNSCheck prints Tuta's DNS verdict for a custom domain (the wizard's
// refresh button). Exit 0 = all records fine, 1 = something missing/invalid.
func DNSCheck(stdout, stderr io.Writer, domain string, opts ReadOpts) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	c := api.NewClient(consts.BaseURL, s.AccessToken)
	res, err := services.CheckDomainDNS(c, domain)
	if err != nil {
		return bail(stderr, err)
	}
	allOK := res.CheckResult == "OK" && len(res.MissingRecords) == 0 && len(res.InvalidRecords) == 0
	if opts.AsJSON {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		fmt.Fprintf(stdout, "%s: %s\n", res.Domain, res.CheckResult)
		for _, r := range res.RequiredRecords {
			status := "OK"
			if containsRecord(res.MissingRecords, r) {
				status = "MISSING"
			} else if containsRecord(res.InvalidRecords, r) {
				status = "INVALID"
			}
			sub := r.Subdomain
			if sub == "" {
				sub = "@"
			}
			fmt.Fprintf(stdout, "  %-8s %-7s %-16s %s\n", status, r.Type, sub, r.Value)
		}
	}
	if allOK {
		return 0
	}
	return 1
}

func containsRecord(list []services.DNSRecord, r services.DNSRecord) bool {
	for _, x := range list {
		if x.Type == r.Type && x.Subdomain == r.Subdomain {
			return true
		}
	}
	return false
}

// VerifyRecord prints the ownership TXT record Tuta expects for domain.
func VerifyRecord(stdout, stderr io.Writer, domain string) int {
	s, err := session.Load()
	if err != nil {
		return bail(stderr, err)
	}
	if s.CustomerID == "" {
		fmt.Fprintln(stderr, "session has no customer_id; log in again")
		return 2
	}
	fmt.Fprintln(stdout, services.DomainValidationRecord(domain, s.CustomerID))
	return 0
}
