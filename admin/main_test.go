package main

import (
	"html/template"
	"testing"
)

// TestTemplatesParse 验证总览与三个管理页共用的模板定义能在服务启动前完整解析。
func TestTemplatesParse(t *testing.T) {
	if _, err := template.ParseFS(templateFiles, "templates/*.html"); err != nil {
		t.Fatalf("解析后台模板失败: %v", err)
	}
}
