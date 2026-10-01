package admin

import _ "embed"

// openAPIDocument 嵌入 OpenAPI 描述，供文档页及 /openapi.json 返回。
//
//go:embed openapi.json
var openAPIDocument []byte
