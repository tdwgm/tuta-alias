// Package typemodel ports tuta_alias/typemodel.py — wire-format pack/unpack
// for Tutanota REST entities (numeric field IDs, string-encoded values).
package typemodel

type FieldSpec struct {
	ID   string
	Kind string
	Card string
}

// Entities mirrors tuta_alias/typemodel.py::ENTITIES (sys app v150).
var Entities = map[string]map[string]FieldSpec{
	"SaltData": {
		"_format":     {"418", "Number", "One"},
		"mailAddress": {"419", "String", "One"},
	},
	"SaltReturn": {
		"_format":    {"421", "Number", "One"},
		"salt":       {"422", "Bytes", "One"},
		"kdfVersion": {"2133", "Number", "One"},
	},
	"CreateSessionData": {
		"_format":             {"1212", "Number", "One"},
		"mailAddress":         {"1213", "String", "ZeroOrOne"},
		"authVerifier":        {"1214", "String", "ZeroOrOne"},
		"clientIdentifier":    {"1215", "String", "One"},
		"accessKey":           {"1216", "Bytes", "ZeroOrOne"},
		"authToken":           {"1217", "String", "ZeroOrOne"},
		"recoverCodeVerifier": {"1417", "String", "ZeroOrOne"},
		"user":                {"1218", "ELEMENT_ASSOCIATION", "ZeroOrOne"},
	},
	"CreateSessionReturn": {
		"_format":     {"1220", "Number", "One"},
		"accessToken": {"1221", "String", "One"},
		"challenges":  {"1222", "AGGREGATION", "Any"},
		"user":        {"1223", "ELEMENT_ASSOCIATION", "One"},
	},
	"SecondFactorAuthData": {
		"_format":  {"542", "Number", "One"},
		"type":     {"1230", "Number", "ZeroOrOne"},
		"otpCode":  {"1243", "Number", "ZeroOrOne"},
		"u2f":      {"1231", "AGGREGATION", "ZeroOrOne"},
		"session":  {"1232", "LIST_ELEMENT_ASSOCIATION_CUSTOM", "ZeroOrOne"},
		"webauthn": {"1905", "AGGREGATION", "ZeroOrOne"},
	},
	"SecondFactorAuthGetData": {
		"_format":     {"1234", "Number", "One"},
		"accessToken": {"1235", "String", "One"},
	},
	"SecondFactorAuthGetReturn": {
		"_format":             {"1237", "Number", "One"},
		"secondFactorPending": {"1238", "Boolean", "One"},
	},
	"MailAddressAliasServiceData": {
		"_format":     {"689", "Number", "One"},
		"mailAddress": {"690", "String", "One"},
		"group":       {"691", "ELEMENT_ASSOCIATION", "One"},
	},
	"MailAddressAliasServiceDataDelete": {
		"_format":     {"786", "Number", "One"},
		"mailAddress": {"787", "String", "One"},
		"restore":     {"788", "Boolean", "One"},
		"group":       {"789", "ELEMENT_ASSOCIATION", "One"},
	},
	"MailAddressAliasGetIn": {
		"_format":     {"2096", "Number", "One"},
		"targetGroup": {"2097", "ELEMENT_ASSOCIATION", "One"},
	},
	"CustomDomainCheckGetIn": {
		"_format":  {"1587", "Number", "One"},
		"domain":   {"1588", "String", "One"},
		"customer": {"2053", "ELEMENT_ASSOCIATION", "ZeroOrOne"},
	},
	"CustomDomainCheckGetOut": {
		"_format":         {"1590", "Number", "One"},
		"checkResult":     {"1591", "Number", "One"},
		"missingRecords":  {"1592", "AGGREGATION", "Any"},
		"invalidRecords":  {"1593", "AGGREGATION", "Any"},
		"requiredRecords": {"1758", "AGGREGATION", "Any"},
	},
	"DnsRecord": {
		"_id":       {"1582", "CustomId", "One"},
		"subdomain": {"1583", "String", "ZeroOrOne"},
		"type":      {"1584", "Number", "One"},
		"value":     {"1585", "String", "One"},
	},
	"MailAddressAliasServiceReturn": {
		"_format":          {"693", "Number", "One"},
		"nbrOfFreeAliases": {"694", "Number", "One"},
		"totalAliases":     {"1069", "Number", "One"},
		"usedAliases":      {"1070", "Number", "One"},
		"enabledAliases":   {"1071", "Number", "One"},
	},
	"User": {
		"_id":         {"86", "GeneratedId", "One"},
		"_format":     {"88", "Number", "One"},
		"salt":        {"90", "Bytes", "ZeroOrOne"},
		"accountType": {"92", "Number", "One"},
		"enabled":     {"93", "Boolean", "One"},
		"kdfVersion":  {"2132", "Number", "One"},
		"userGroup":   {"95", "AGGREGATION", "One"},
		"memberships": {"96", "AGGREGATION", "Any"},
		"customer":    {"99", "ELEMENT_ASSOCIATION", "ZeroOrOne"},
	},
	"Customer": {
		"_id":          {"33", "GeneratedId", "One"},
		"_format":      {"35", "Number", "One"},
		"type":         {"36", "Number", "One"},
		"customerInfo": {"160", "LIST_ELEMENT_ASSOCIATION_GENERATED", "One"},
	},
	"CustomerInfo": {
		"_id":         {"150", "GeneratedId", "One"},
		"_format":     {"152", "Number", "One"},
		"domain":      {"154", "String", "One"},
		"domainInfos": {"726", "AGGREGATION", "Any"},
	},
	"GroupInfo": {
		"_id":                {"16", "GeneratedId", "One"},
		"_format":            {"18", "Number", "One"},
		"mailAddress":        {"22", "String", "ZeroOrOne"},
		"group":              {"20", "ELEMENT_ASSOCIATION", "One"},
		"mailAddressAliases": {"687", "AGGREGATION", "Any"},
	},
	"DomainInfo": {
		"_id":    {"697", "CustomId", "One"},
		"domain": {"698", "String", "One"},
	},
	"MailAddressAlias": {
		"_id":         {"685", "CustomId", "One"},
		"mailAddress": {"686", "String", "One"},
		"enabled":     {"784", "Boolean", "One"},
	},
	"GroupMembership": {
		"_id":       {"26", "CustomId", "One"},
		"admin":     {"28", "Boolean", "One"},
		"groupType": {"1030", "Number", "ZeroOrOne"},
		"group":     {"29", "ELEMENT_ASSOCIATION", "One"},
		"groupInfo": {"30", "LIST_ELEMENT_ASSOCIATION_GENERATED", "One"},
	},
}

// AggregationRefs maps (parent, fieldName) → child entity for nested decoding.
var AggregationRefs = map[[2]string]string{
	{"CustomerInfo", "domainInfos"}:                "DomainInfo",
	{"GroupInfo", "mailAddressAliases"}:            "MailAddressAlias",
	{"User", "memberships"}:                        "GroupMembership",
	{"CustomDomainCheckGetOut", "missingRecords"}:  "DnsRecord",
	{"CustomDomainCheckGetOut", "invalidRecords"}:  "DnsRecord",
	{"CustomDomainCheckGetOut", "requiredRecords"}: "DnsRecord",
	{"User", "userGroup"}:                          "GroupMembership",
}

func isScalarKind(k string) bool {
	switch k {
	case "String", "Number", "Boolean", "Bytes", "Date", "GeneratedId", "CustomId":
		return true
	}
	return false
}

func isAssocKind(k string) bool {
	switch k {
	case "ELEMENT_ASSOCIATION", "LIST_ELEMENT_ASSOCIATION_GENERATED",
		"LIST_ELEMENT_ASSOCIATION_CUSTOM", "LIST_ASSOCIATION", "AGGREGATION":
		return true
	}
	return false
}
