package model

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
)

// DeepSeek uses XML-like DSML tags. Only a complete function_calls root is
// executable, with invoke/parameter children; arbitrary XML is never scanned.
func parseXMLCalls(text string) ([]Call, error) {
	text = strings.ReplaceAll(text, "<｜DSML｜", "<")
	text = strings.ReplaceAll(text, "</｜DSML｜", "</")
	dec := xml.NewDecoder(strings.NewReader(text))
	tok, err := dec.Token()
	if err != nil {
		return nil, ErrToolProtocol
	}
	root, ok := tok.(xml.StartElement)
	if !ok || root.Name.Space != "" || root.Name.Local != "function_calls" || len(root.Attr) != 0 {
		return nil, ErrToolProtocol
	}
	var calls []Call
	for {
		tok, err = xmlToken(dec)
		if err != nil {
			return nil, ErrToolProtocol
		}
		if end, ok := tok.(xml.EndElement); ok {
			if end.Name != root.Name {
				return nil, ErrToolProtocol
			}
			break
		}
		invoke, ok := tok.(xml.StartElement)
		if !ok || invoke.Name.Space != "" || invoke.Name.Local != "invoke" {
			return nil, ErrToolProtocol
		}
		attrs, err := xmlAttrs(invoke, "name", "id")
		if err != nil || attrs["name"] == "" {
			return nil, ErrToolProtocol
		}
		call := Call{Name: attrs["name"], ID: attrs["id"]}
		args := map[string]json.RawMessage{}
		for {
			tok, err = xmlToken(dec)
			if err != nil {
				return nil, ErrToolProtocol
			}
			if end, ok := tok.(xml.EndElement); ok {
				if end.Name != invoke.Name {
					return nil, ErrToolProtocol
				}
				break
			}
			param, ok := tok.(xml.StartElement)
			if !ok || param.Name.Space != "" || param.Name.Local != "parameter" {
				return nil, ErrToolProtocol
			}
			attrs, err := xmlAttrs(param, "name", "string")
			if err != nil || attrs["name"] == "" || args[attrs["name"]] != nil {
				return nil, ErrToolProtocol
			}
			var value strings.Builder
			for {
				tok, err = dec.Token()
				if err != nil {
					return nil, ErrToolProtocol
				}
				if end, ok := tok.(xml.EndElement); ok {
					if end.Name != param.Name {
						return nil, ErrToolProtocol
					}
					break
				}
				chars, ok := tok.(xml.CharData)
				if !ok {
					return nil, ErrToolProtocol
				}
				value.Write(chars)
			}
			var raw []byte
			switch attrs["string"] {
			case "true":
				raw, _ = json.Marshal(value.String())
			case "false":
				raw = []byte(strings.TrimSpace(value.String()))
				if StrictJSON(raw) != nil {
					return nil, ErrToolProtocol
				}
			default:
				return nil, ErrToolProtocol
			}
			args[attrs["name"]] = raw
		}
		raw, _ := json.Marshal(args)
		call.Arguments = string(raw)
		calls = append(calls, call)
	}
	if _, err := xmlToken(dec); err != io.EOF || len(calls) == 0 {
		return nil, ErrToolProtocol
	}
	return calls, nil
}

func xmlToken(dec *xml.Decoder) (xml.Token, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if text, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(text)) == "" {
			continue
		}
		return tok, nil
	}
}

func xmlAttrs(start xml.StartElement, allowed ...string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range start.Attr {
		valid := false
		for _, name := range allowed {
			if a.Name.Space == "" && a.Name.Local == name {
				valid = true
			}
		}
		if _, exists := out[a.Name.Local]; !valid || exists {
			return nil, ErrToolProtocol
		}
		out[a.Name.Local] = a.Value
	}
	return out, nil
}
