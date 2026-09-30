package admin

import _ "embed"

// openAPIDocument 是编译期打包进二进制的 OpenAPI 描述文件,内容来自 admin 目录下的 openapi.json。
// 文档页面和 /openapi.json 直接输出这份字节,部署机上不用再单独放一份文件。
//
//go:embed openapi.json
var openAPIDocument []byte
