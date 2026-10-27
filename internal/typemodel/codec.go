package typemodel

import (
	"encoding/base64"
	"fmt"
	"strconv"
)

func encScalar(value any, kind string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch kind {
	case "Boolean":
		if b, ok := value.(bool); ok {
			if b {
				return "1", nil
			}
			return "0", nil
		}
		return "0", nil
	case "Number":
		switch v := value.(type) {
		case int:
			return strconv.Itoa(v), nil
		case int64:
			return strconv.FormatInt(v, 10), nil
		case uint64:
			return strconv.FormatUint(v, 10), nil
		case float64:
			return strconv.FormatInt(int64(v), 10), nil
		case string:
			return v, nil
		}
		return fmt.Sprintf("%v", value), nil
	case "Bytes":
		switch v := value.(type) {
		case string:
			return v, nil
		case []byte:
			return base64.StdEncoding.EncodeToString(v), nil
		}
		return nil, fmt.Errorf("bytes: unsupported value type %T", value)
	case "Date":
		switch v := value.(type) {
		case int64:
			return strconv.FormatInt(v, 10), nil
		case int:
			return strconv.Itoa(v), nil
		}
		return fmt.Sprintf("%v", value), nil
	}
	return fmt.Sprintf("%v", value), nil
}

func encAssoc(value any, kind, card string) ([]any, error) {
	if value == nil {
		return []any{}, nil
	}
	switch kind {
	case "AGGREGATION":
		switch v := value.(type) {
		case []any:
			return v, nil
		case map[string]any:
			return []any{v}, nil
		}
		return []any{value}, nil
	case "LIST_ELEMENT_ASSOCIATION_GENERATED", "LIST_ELEMENT_ASSOCIATION_CUSTOM":
		switch v := value.(type) {
		case []string:
			if len(v) == 2 {
				return []any{[]any{v[0], v[1]}}, nil
			}
		case [2]string:
			return []any{[]any{v[0], v[1]}}, nil
		case []any:
			if len(v) == 2 {
				return []any{v}, nil
			}
		}
		return nil, fmt.Errorf("list-element assoc: bad value %#v", value)
	}
	switch v := value.(type) {
	case string:
		return []any{v}, nil
	case []any:
		return v, nil
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out, nil
	}
	return []any{value}, nil
}

// Pack converts a friendly dict to the numeric-field-id JSON envelope.
func Pack(entityName string, data map[string]any) (map[string]any, error) {
	spec, ok := Entities[entityName]
	if !ok {
		return nil, fmt.Errorf("unknown entity %q", entityName)
	}
	out := make(map[string]any, len(spec))
	for name, fs := range spec {
		v, present := data[name]
		switch {
		case isScalarKind(fs.Kind):
			if !present && fs.Card == "One" {
				return nil, fmt.Errorf("%s.%s is required (cardinality One)", entityName, name)
			}
			if !present {
				out[fs.ID] = nil
				continue
			}
			enc, err := encScalar(v, fs.Kind)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", entityName, name, err)
			}
			out[fs.ID] = enc
		case isAssocKind(fs.Kind):
			if !present && fs.Card == "One" {
				return nil, fmt.Errorf("%s.%s is required (cardinality One)", entityName, name)
			}
			if !present {
				out[fs.ID] = []any{}
				continue
			}
			enc, err := encAssoc(v, fs.Kind, fs.Card)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", entityName, name, err)
			}
			out[fs.ID] = enc
		}
	}
	return out, nil
}

func decScalar(raw any, kind string) (any, error) {
	if raw == nil {
		return nil, nil
	}
	switch kind {
	case "Boolean":
		if s, ok := raw.(string); ok {
			return s == "1", nil
		}
		if b, ok := raw.(bool); ok {
			return b, nil
		}
		return false, nil
	case "Number":
		if s, ok := raw.(string); ok {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, err
			}
			return int(n), nil
		}
		return raw, nil
	case "Bytes":
		if s, ok := raw.(string); ok {
			if s == "" {
				return []byte{}, nil
			}
			return base64.StdEncoding.DecodeString(s)
		}
		return raw, nil
	case "Date":
		if s, ok := raw.(string); ok {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, err
			}
			return n, nil
		}
		return raw, nil
	}
	return raw, nil
}

func decAssoc(parent, fieldName string, raw any, kind, card string) (any, error) {
	if raw == nil {
		if card == "ZeroOrOne" {
			return nil, nil
		}
		return []any{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return raw, nil
	}
	if len(items) == 0 {
		if card == "ZeroOrOne" {
			return nil, nil
		}
		return []any{}, nil
	}
	switch kind {
	case "AGGREGATION":
		ref, hasRef := AggregationRefs[[2]string{parent, fieldName}]
		if hasRef {
			out := make([]map[string]any, 0, len(items))
			for _, it := range items {
				m, ok := it.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s.%s: aggregation item not object", parent, fieldName)
				}
				u, err := Unpack(ref, m)
				if err != nil {
					return nil, err
				}
				out = append(out, u)
			}
			return out, nil
		}
		return items, nil
	case "LIST_ELEMENT_ASSOCIATION_GENERATED", "LIST_ELEMENT_ASSOCIATION_CUSTOM":
		if card == "One" && len(items) == 1 {
			if pair, ok := items[0].([]any); ok && len(pair) == 2 {
				a, _ := pair[0].(string)
				b, _ := pair[1].(string)
				return []string{a, b}, nil
			}
		}
		return items, nil
	}
	if card == "One" && len(items) == 1 {
		if s, ok := items[0].(string); ok {
			return s, nil
		}
	}
	if card == "ZeroOrOne" {
		if s, ok := items[0].(string); ok {
			return s, nil
		}
	}
	return items, nil
}

// Unpack converts the numeric-field-id JSON envelope back to a friendly map.
func Unpack(entityName string, raw map[string]any) (map[string]any, error) {
	spec, ok := Entities[entityName]
	if !ok {
		return nil, fmt.Errorf("unknown entity %q", entityName)
	}
	inverted := make(map[string]struct {
		Name string
		Kind string
		Card string
	}, len(spec))
	for name, fs := range spec {
		inverted[fs.ID] = struct {
			Name string
			Kind string
			Card string
		}{name, fs.Kind, fs.Card}
	}
	out := make(map[string]any, len(spec))
	for fid, v := range raw {
		meta, ok := inverted[fid]
		if !ok {
			continue
		}
		switch {
		case isScalarKind(meta.Kind):
			dv, err := decScalar(v, meta.Kind)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", entityName, meta.Name, err)
			}
			out[meta.Name] = dv
		case isAssocKind(meta.Kind):
			dv, err := decAssoc(entityName, meta.Name, v, meta.Kind, meta.Card)
			if err != nil {
				return nil, err
			}
			out[meta.Name] = dv
		}
	}
	return out, nil
}
