package mcpgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode"
	"unicode/utf8"
)

const maxNesting = 64

var (
	errAmbiguousJSON = errors.New("mcpgateway: an object repeats a key, exactly or in another case")
	errMalformedJSON = errors.New("mcpgateway: not one well-formed JSON value")
	errNotAMessage   = errors.New("mcpgateway: not a JSON-RPC message")
)

var (
	messageFields = map[string]bool{"jsonrpc": true, "id": true, "method": true, "params": true, "result": true, "error": true}
	errorFields   = map[string]bool{"code": true, "message": true, "data": true}
)

func decodeStrict(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errMalformedJSON
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := strictValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errMalformedJSON
	}
	return v, nil
}

func strictValue(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, errMalformedJSON
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if depth >= maxNesting {
		return nil, errMalformedJSON
	}
	switch delim {
	case '{':
		return strictObject(dec, depth+1)
	case '[':
		return strictArray(dec, depth+1)
	}
	return nil, errMalformedJSON
}

func strictObject(dec *json.Decoder, depth int) (map[string]any, error) {
	out := map[string]any{}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return nil, errMalformedJSON
		}
		folded := foldKey(key)
		if seen[folded] {
			return nil, errAmbiguousJSON
		}
		seen[folded] = true
		v, err := strictValue(dec, depth)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errMalformedJSON
	}
	return out, nil
}

func strictArray(dec *json.Decoder, depth int) ([]any, error) {
	out := []any{}
	for dec.More() {
		v, err := strictValue(dec, depth)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		return nil, errMalformedJSON
	}
	return out, nil
}

func foldKey(key string) string {
	var b []rune
	for _, r := range key {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		b = append(b, least)
	}
	return string(b)
}

func canonical(v any) (json.RawMessage, error) {
	return json.Marshal(v)
}

func strictMessage(raw []byte, fields map[string]bool) (rpcMessage, error) {
	v, err := decodeStrict(raw)
	if err != nil {
		return rpcMessage{}, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return rpcMessage{}, errNotAMessage
	}
	for k := range obj {
		if !fields[k] {
			return rpcMessage{}, errNotAMessage
		}
	}
	var m rpcMessage
	if m.JSONRPC, ok = optionalString(obj, "jsonrpc"); !ok {
		return rpcMessage{}, errNotAMessage
	}
	if m.Method, ok = optionalString(obj, "method"); !ok {
		return rpcMessage{}, errNotAMessage
	}
	if id, present := obj["id"]; present {
		switch id.(type) {
		case string, json.Number, nil:
		default:
			return rpcMessage{}, errNotAMessage
		}
		if m.ID, err = canonical(id); err != nil {
			return rpcMessage{}, errNotAMessage
		}
	}
	switch p := obj["params"].(type) {
	case nil:
	case map[string]any, []any:
		if m.Params, err = canonical(p); err != nil {
			return rpcMessage{}, errNotAMessage
		}
	default:
		return rpcMessage{}, errNotAMessage
	}
	if r, present := obj["result"]; present {
		if m.Result, err = canonical(r); err != nil {
			return rpcMessage{}, errNotAMessage
		}
	}
	if e, present := obj["error"]; present {
		if m.Error, err = strictError(e); err != nil {
			return rpcMessage{}, err
		}
	}
	return m, nil
}

func optionalString(obj map[string]any, key string) (string, bool) {
	v, present := obj[key]
	if !present {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

func strictError(v any) (*rpcError, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errNotAMessage
	}
	for k := range obj {
		if !errorFields[k] {
			return nil, errNotAMessage
		}
	}
	n, ok := obj["code"].(json.Number)
	if !ok {
		return nil, errNotAMessage
	}
	code, err := n.Int64()
	if err != nil {
		return nil, errNotAMessage
	}
	message, ok := obj["message"].(string)
	if !ok {
		return nil, errNotAMessage
	}
	out := &rpcError{Code: int(code), Message: message}
	if data, present := obj["data"]; present {
		if out.Data, err = canonical(data); err != nil {
			return nil, errNotAMessage
		}
	}
	return out, nil
}

func toolName(params json.RawMessage) (string, bool) {
	v, err := decodeStrict(params)
	if err != nil {
		return "", false
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	name, ok := obj["name"].(string)
	return name, ok && name != ""
}
