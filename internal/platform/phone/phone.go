// Package phone 提供中国大陆手机号的归一化、格式校验和展示脱敏。
package phone

import (
	"regexp"
	"strings"
)

var mobile = regexp.MustCompile(`^1[3-9][0-9]{9}$`)

// Normalize 去除号码首尾空白，不转换区号或分隔符。
func Normalize(value string) string { return strings.TrimSpace(value) }

// Valid 校验去除首尾空白后的 11 位中国大陆手机号格式。
func Valid(value string) bool { return mobile.MatchString(Normalize(value)) }

// Mask 保留号码前 3 位和后 4 位，中间使用星号；不足 7 位时返回 ***。
func Mask(value string) string {
	value = Normalize(value)
	if len(value) < 7 {
		return "***"
	}
	return value[:3] + "****" + value[len(value)-4:]
}
