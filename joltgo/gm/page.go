package gm

import _ "embed"

// indexPage 是内嵌的单页操作界面。用 go:embed 而不是运行时读文件：
// 单二进制部署，`joltgo.exe -type gm` 在哪跑都能起页面，不需要带着 assets 目录。
//
//go:embed index.html
var indexPage []byte
