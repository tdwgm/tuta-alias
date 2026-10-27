package typemodel

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPack_SaltData(t *testing.T) {
	got, err := Pack("SaltData", map[string]any{
		"_format":     0,
		"mailAddress": "u@example.com",
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	want := map[string]any{
		"418": "0",
		"419": "u@example.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pack mismatch\n got=%v\nwant=%v", got, want)
	}
}

func TestPack_BooleanAsStringOneZero(t *testing.T) {
	got, err := Pack("MailAddressAlias", map[string]any{
		"_id":         "abc",
		"mailAddress": "x@y",
		"enabled":     true,
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if got["784"] != "1" {
		t.Fatalf("enabled: want \"1\" got %v", got["784"])
	}
}

func TestUnpack_DomainInfoNestedAggregation(t *testing.T) {
	raw := map[string]any{
		"150": "elemid",
		"152": "0",
		"154": "example.com",
		"726": []any{
			map[string]any{
				"697": "x",
				"698": "example.com",
			},
		},
	}
	got, err := Unpack("CustomerInfo", raw)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if got["domain"] != "example.com" {
		t.Fatalf("domain: %v", got["domain"])
	}
	dInfos, ok := got["domainInfos"].([]map[string]any)
	if !ok || len(dInfos) != 1 || dInfos[0]["domain"] != "example.com" {
		b, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("domainInfos mismatch:\n%s", b)
	}
}

func TestPack_ListElementAssociationAsPair(t *testing.T) {
	got, err := Pack("GroupMembership", map[string]any{
		"_id":       "0",
		"admin":     false,
		"groupType": 5,
		"group":     "groupID",
		"groupInfo": []string{"listid", "elemid"},
	})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	pairs, ok := got["30"].([]any)
	if !ok || len(pairs) != 1 {
		t.Fatalf("groupInfo pairs: %v", got["30"])
	}
	pair, _ := pairs[0].([]any)
	if len(pair) != 2 || pair[0] != "listid" || pair[1] != "elemid" {
		t.Fatalf("pair shape: %v", pair)
	}
}

func TestPack_RequiredFieldMissing(t *testing.T) {
	_, err := Pack("SaltData", map[string]any{"_format": 0})
	if err == nil {
		t.Fatalf("expected error for missing mailAddress")
	}
}

func TestUnpack_SaltReturn_DecodesBytes(t *testing.T) {
	raw := map[string]any{
		"421":  "0",
		"422":  "AAEC", // base64 of [0,1,2]
		"2133": "1",
	}
	got, err := Unpack("SaltReturn", raw)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}
	salt, ok := got["salt"].([]byte)
	if !ok || len(salt) != 3 || salt[0] != 0 || salt[1] != 1 || salt[2] != 2 {
		t.Fatalf("salt: %v", got["salt"])
	}
	if got["kdfVersion"] != 1 {
		t.Fatalf("kdfVersion: %v", got["kdfVersion"])
	}
}

func TestUnpackCustomDomainCheckGetOut(t *testing.T) {
	raw := map[string]any{
		"1590": "0",
		"1591": "0",
		"1592": []any{map[string]any{"1582": "a", "1583": "s2._domainkey", "1584": "2", "1585": "s2.domainkey.tutanota.de"}},
		"1593": []any{},
		"1758": []any{map[string]any{"1582": "b", "1583": nil, "1584": "0", "1585": "mail.tutanota.de"}},
	}
	out, err := Unpack("CustomDomainCheckGetOut", raw)
	if err != nil {
		t.Fatal(err)
	}
	if out["checkResult"] != 0 {
		t.Errorf("checkResult = %v", out["checkResult"])
	}
	missing, ok := out["missingRecords"].([]map[string]any)
	if !ok || len(missing) != 1 || missing[0]["type"] != 2 || missing[0]["subdomain"] != "s2._domainkey" {
		t.Errorf("missingRecords = %#v", out["missingRecords"])
	}
	req, ok := out["requiredRecords"].([]map[string]any)
	if !ok || len(req) != 1 || req[0]["subdomain"] != nil || req[0]["value"] != "mail.tutanota.de" {
		t.Errorf("requiredRecords = %#v", out["requiredRecords"])
	}
}

// TestAggregationRefsCoverConsumers pins the aggregation fields whose callers
// assert []map[string]any. decAssoc only unpacks an AGGREGATION into that
// shape when AggregationRefs has an entry for (parent, field); without one it
// returns the raw []any, the assertion in internal/services fails silently and
// the user sees "no custom domains" or an empty alias list instead of an
// error. The three fields left out of AggregationRefs on purpose are
// SecondFactorAuthData.u2f and .webauthn (encode-only) and
// CreateSessionReturn.challenges, which internal/login reads as []any and only
// measures the length of.
func TestAggregationRefsCoverConsumers(t *testing.T) {
	needRef := [][2]string{
		{"CustomerInfo", "domainInfos"},                // services.ListDomains
		{"GroupInfo", "mailAddressAliases"},            // services.ListAddresses
		{"CustomDomainCheckGetOut", "requiredRecords"}, // services.dnsRecords
		{"CustomDomainCheckGetOut", "missingRecords"},
		{"CustomDomainCheckGetOut", "invalidRecords"},
		{"User", "memberships"}, // login.go, the mail group lookup
	}
	for _, key := range needRef {
		fs, ok := Entities[key[0]][key[1]]
		if !ok {
			t.Errorf("%s.%s is gone from the entity table", key[0], key[1])
			continue
		}
		if fs.Kind != "AGGREGATION" {
			t.Errorf("%s.%s is %s, not AGGREGATION", key[0], key[1], fs.Kind)
		}
		if _, ok := AggregationRefs[key]; !ok {
			t.Errorf("%s.%s has no AggregationRefs entry, so it decodes to []any "+
				"and its caller's []map[string]any assertion fails silently", key[0], key[1])
		}
	}
}
