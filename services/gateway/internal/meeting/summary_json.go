package meeting

import (
	"bytes"
	"encoding/json"
)

// 兼容旧假纪要：决策/风险曾是字符串数组，待办也曾是字符串数组。

func (c *CitedItem) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		c.Text = s
		return nil
	}
	type alias CitedItem
	return json.Unmarshal(b, (*alias)(c))
}

func (a *ActionItem) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		a.Task = s
		return nil
	}
	type alias ActionItem
	return json.Unmarshal(b, (*alias)(a))
}
