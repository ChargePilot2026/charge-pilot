package charge

import "slices"

// oneOfStatus 校验查询状态参数：空值放行，否则必须是允许列表里的词。
func oneOfStatus(value, allowed string) bool {
	if value == "" {
		return true
	}
	return slices.Contains(splitFields(allowed), value)
}

func splitFields(value string) []string {
	out := []string{}
	current := ""
	for _, r := range value {
		if r == ' ' {
			if current != "" {
				out = append(out, current)
			}
			current = ""
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}
