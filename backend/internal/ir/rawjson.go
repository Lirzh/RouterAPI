package ir

import (
	"bytes"
	"encoding/json"
	"errors"
)

// 直通路径（同格式不转换）用的 JSON 原文操作原语。
//
// 之所以放在 ir 包：这里关心的是"改一个键、其余字节一律不动"，与
// Extra / MarshalWithExtra 解决的是同一个保真度问题，只是粒度更粗。

// ErrNotJSONObject 字节流不是一个 JSON 对象，无法按键改写。
var ErrNotJSONObject = errors.New("body 不是 JSON 对象")

// ParseJSONObject 把 JSON 对象拆成"顶层键 -> 值原文"。
//
// 值保留为 json.RawMessage，所以嵌套内容一个字节都不会被改动；只有顶层键的顺序
// 会按键名重排。JSON 语义与对象键顺序无关，上游网关和各家 SDK 都不依赖它。
func ParseJSONObject(body []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	// "null" 能成功反序列化成 nil map，但它不是对象，不能当直通请求体用。
	if obj == nil {
		return nil, ErrNotJSONObject
	}
	return obj, nil
}

// MarshalJSONObject 序列化，并关掉 HTML 转义。
//
// 直通的意义就在于不改客户端/上游的字节：encoding/json 默认会把正文里的
// < > & 转义成 \u003c 之类，语义等价但字节已经变了。
func MarshalJSONObject(obj map[string]json.RawMessage) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// JSONString 把 s 编码成 JSON 字符串字面量，可直接作为 RawMessage 写进对象。
func JSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
