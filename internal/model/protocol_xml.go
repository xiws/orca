package model

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
)

// parseXMLCalls 解析 DeepSeek DSML XML 格式的工具调用。
// 仅接受完整的 function_calls 根元素及其 invoke/parameter 子元素，
// 不扫描任意 XML。
func parseXMLCalls(text string) ([]Call, error) {
	// 将 DSML 特殊标记替换为标准 XML 标签。
	text = strings.ReplaceAll(text, "<｜DSML｜", "<")
	text = strings.ReplaceAll(text, "</｜DSML｜", "</")
	dec := xml.NewDecoder(strings.NewReader(text))
	tok, err := dec.Token()
	if err != nil {
		return nil, ErrToolProtocol
	}
	// 校验根元素必须为 function_calls。
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
		// 遇到根元素结束标签则退出。
		if end, ok := tok.(xml.EndElement); ok {
			if end.Name != root.Name {
				return nil, ErrToolProtocol
			}
			break
		}
		// 每个 invoke 元素代表一次工具调用。
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
		// 解析 invoke 内的 parameter 子元素。
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
			// 读取 parameter 的文本内容。
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
			// 根据 string 属性决定值的 JSON 编码方式。
			var raw []byte
			switch attrs["string"] {
			case "true":
				// 字符串类型：JSON 编码。
				raw, _ = json.Marshal(value.String())
			case "false":
				// 非字符串类型：直接作为 JSON 值，需通过严格校验。
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
	// 校验根元素后必须为 EOF，且至少有一个调用。
	if _, err := xmlToken(dec); err != io.EOF || len(calls) == 0 {
		return nil, ErrToolProtocol
	}
	return calls, nil
}

// xmlToken 从 XML 解码器中读取下一个非空白文本的 token。
func xmlToken(dec *xml.Decoder) (xml.Token, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		// 跳过空白文本节点。
		if text, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(text)) == "" {
			continue
		}
		return tok, nil
	}
}

// xmlAttrs 从 XML 起始元素中提取指定名称的属性，拒绝未知属性和重复属性。
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
