package platform

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
)

func (a *App) startupError(ctx context.Context, id string, cause error) string {
	if errors.Is(cause, context.DeadlineExceeded) {
		return "MCP 启动或连接验证超时；请检查首次依赖下载、网络和运行环境后重试"
	}
	i, err := a.loadInstance(ctx, id)
	if err != nil {
		return "无法读取 MCP 配置，请刷新后重试"
	}
	return "MCP 启动失败：" + redactStartupDiagnostic(i, cause.Error())
}

// instanceDiagnosticValues includes private transport credentials without ever
// adding them to the JSON configuration, editor response, or audit payload.
func instanceDiagnosticValues(i Instance) []string {
	values := append([]string{i.URL, i.HeaderHelper}, i.Args...)
	if i.diagnosticSecrets != nil {
		values = append(values, i.diagnosticSecrets()...)
	}
	return values
}

func redactStartupDiagnostic(i Instance, detail string) string {
	values := instanceDiagnosticValues(i)
	for _, v := range i.Env {
		values = append(values, v)
	}
	for _, v := range i.Headers {
		values = append(values, v)
		values = append(values, strings.TrimPrefix(v, "Bearer "))
	}
	sort.Slice(values, func(a, b int) bool { return len(values[a]) > len(values[b]) })
	for _, value := range values {
		if len(value) >= 4 {
			detail = strings.ReplaceAll(detail, value, "<REDACTED>")
		}
	}
	for _, value := range append([]string{i.URL}, i.Args...) {
		if u, err := url.Parse(value); err == nil && u.User != nil {
			if password, ok := u.User.Password(); ok && password != "" {
				detail = strings.ReplaceAll(detail, password, "<REDACTED>")
				detail = strings.ReplaceAll(detail, url.QueryEscape(password), "<REDACTED>")
			}
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if text := []rune(detail); len(text) > 800 {
		detail = string(text[:800]) + "…"
	}
	return detail
}
