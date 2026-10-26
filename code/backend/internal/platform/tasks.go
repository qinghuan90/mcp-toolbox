package platform

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const defaultCatalogRegistry = "https://registry.modelcontextprotocol.io"

type catalogSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type catalogSourceResult struct {
	Name       string           `json:"name"`
	URL        string           `json:"url"`
	Count      int              `json:"count"`
	Error      string           `json:"error,omitempty"`
	NextCursor string           `json:"next_cursor,omitempty"`
	Items      []map[string]any `json:"-"`
}

func (a *App) catalogSources() []catalogSource {
	a.registryMu.RLock()
	defer a.registryMu.RUnlock()
	if len(a.catalogRegistrySources) > 0 {
		return append([]catalogSource(nil), a.catalogRegistrySources...)
	}
	value := a.catalogRegistryURL
	if value == "" {
		value = defaultCatalogRegistry
	}
	return []catalogSource{{Name: "MCP Registry", URL: value}}
}
func (a *App) catalogRegistry() string { return a.catalogSources()[0].URL }

func normalizeCatalogSources(sources []catalogSource) ([]catalogSource, error) {
	if len(sources) == 0 {
		sources = []catalogSource{{Name: "MCP Registry", URL: defaultCatalogRegistry}}
	}
	if len(sources) > 8 {
		return nil, errors.New("最多配置 8 个目录仓库")
	}
	seen := map[string]bool{}
	out := []catalogSource{}
	for _, source := range sources {
		source.URL = strings.TrimRight(strings.TrimSpace(source.URL), "/")
		u, e := url.Parse(source.URL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(source.URL) > 500 {
			return nil, errors.New("目录仓库须为无凭据、无查询参数的 HTTPS API 根地址")
		}
		source.Name = strings.TrimSpace(source.Name)
		if source.Name == "" {
			source.Name = u.Host
		}
		if len(source.Name) > 100 {
			return nil, errors.New("目录仓库名称过长")
		}
		if !seen[source.URL] {
			seen[source.URL] = true
			out = append(out, source)
		}
	}
	return out, nil
}

func (a *App) reloadCatalogRegistry(ctx context.Context) error {
	var value string
	err := a.DB.QueryRow(ctx, "SELECT value FROM system_settings WHERE key='catalog_registries'").Scan(&value)
	var sources []catalogSource
	if err == nil {
		if err = json.Unmarshal([]byte(value), &sources); err != nil {
			return err
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		err = a.DB.QueryRow(ctx, "SELECT value FROM system_settings WHERE key='catalog_registry'").Scan(&value)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if strings.TrimSpace(value) != "" {
			sources = []catalogSource{{Name: "MCP Registry", URL: value}}
		}
	} else {
		return err
	}
	sources, err = normalizeCatalogSources(sources)
	if err != nil {
		return err
	}
	a.registryMu.Lock()
	a.catalogRegistrySources = sources
	a.catalogRegistryURL = sources[0].URL
	a.registryMu.Unlock()
	return nil
}

type catalogServer struct {
	Server map[string]any `json:"server"`
}

func (a *App) fetchCatalogPage(ctx context.Context, target string) ([]map[string]any, string, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if e != nil {
		return nil, "", e
	}
	req.Header.Set("Accept", "application/json")
	client := *http.DefaultClient
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("仓库重定向超过 3 次")
		}
		if next.URL.Scheme != "https" || next.URL.User != nil {
			return errors.New("仓库重定向必须为无凭据的 HTTPS 地址")
		}
		return nil
	}
	res, e := client.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("仓库返回 HTTP %d", res.StatusCode)
	}
	var data struct {
		Servers  []catalogServer `json:"servers"`
		Metadata struct {
			NextCursor string `json:"nextCursor"`
		} `json:"metadata"`
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&data); e != nil {
		return nil, "", errors.New("响应不是有效的 Registry JSON")
	}
	if data.Servers == nil {
		return nil, "", errors.New("响应缺少 servers 数组，不兼容 MCP Registry API")
	}
	items := []map[string]any{}
	for _, entry := range data.Servers {
		if entry.Server == nil || entry.Server["name"] == nil || entry.Server["version"] == nil {
			return nil, "", errors.New("仓库条目缺少 server.name 或 version")
		}
		items = append(items, entry.Server)
	}
	if len(data.Metadata.NextCursor) > 4096 {
		return nil, "", errors.New("仓库分页游标过长")
	}
	return items, data.Metadata.NextCursor, nil
}

func (a *App) queryCatalogSources(ctx context.Context, sources []catalogSource, query string, cursors map[string]string, probe bool) []catalogSourceResult {
	results := make([]catalogSourceResult, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		results[i] = catalogSourceResult{Name: source.Name, URL: source.URL}
		cursor, selected := cursors[source.URL]
		if cursors != nil && !selected {
			continue
		}
		wg.Add(1)
		go func(i int, source catalogSource, cursor string) {
			defer wg.Done()
			limit := "100"
			if probe {
				limit = "1"
			}
			params := url.Values{"limit": {limit}, "version": {"latest"}}
			if query != "" {
				params.Set("search", query)
			}
			if cursor != "" {
				params.Set("cursor", cursor)
			}
			sourceCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			items, next, e := a.fetchCatalogPage(sourceCtx, source.URL+"/v0.1/servers?"+params.Encode())
			if e != nil {
				results[i].Error = e.Error()
				return
			}
			if next != "" && next == cursor {
				results[i].Error = "仓库返回重复分页游标，已停止继续加载"
				next = ""
			}
			results[i].Items = items
			results[i].Count = len(items)
			results[i].NextCursor = next
		}(i, source, cursor)
	}
	wg.Wait()
	return results
}

func (a *App) catalog(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		fail(w, 400, "搜索内容过长")
		return
	}
	var cursors map[string]string
	if encoded := r.URL.Query().Get("cursors"); encoded != "" {
		if len(encoded) > 50000 {
			fail(w, 400, "分页参数过长")
			return
		}
		data, e := base64.RawURLEncoding.DecodeString(encoded)
		if e != nil || json.Unmarshal(data, &cursors) != nil || cursors == nil || len(cursors) > 8 {
			fail(w, 400, "无效的分页参数")
			return
		}
		for _, cursor := range cursors {
			if len(cursor) > 4096 {
				fail(w, 400, "分页游标过长")
				return
			}
		}
	}
	sources := a.catalogSources()
	results := a.queryCatalogSources(r.Context(), sources, query, cursors, false)
	items := []map[string]any{}
	seen := map[string]map[string]any{}
	next := map[string]string{}
	for _, result := range results {
		if result.NextCursor != "" {
			next[result.URL] = result.NextCursor
		}
		// Failed sources remain retryable on the next explicit load-more request.
		if result.Error != "" {
			next[result.URL] = ""
			if cursors != nil {
				next[result.URL] = cursors[result.URL]
			}
		}
		for _, item := range result.Items {
			source := map[string]string{"name": result.Name, "url": result.URL}
			key := fmt.Sprint(item["name"]) + "\x00" + fmt.Sprint(item["version"])
			if existing, ok := seen[key]; ok {
				existing["catalog_sources"] = append(existing["catalog_sources"].([]map[string]string), source)
				continue
			}
			item["catalog_sources"] = []map[string]string{source}
			seen[key] = item
			items = append(items, item)
		}
	}
	nextCursor := ""
	if len(next) > 0 {
		raw, _ := json.Marshal(next)
		nextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	respond(w, 200, map[string]any{"items": items, "total": len(items), "sources": results, "next_cursor": nextCursor, "search_scope": "name"})
}

func (a *App) systemCatalogRegistry(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		respond(w, 200, map[string]any{"registry": a.catalogRegistry(), "registries": a.catalogSources(), "default": defaultCatalogRegistry})
		return
	}
	var in struct {
		Registry   string          `json:"registry"`
		Registries []catalogSource `json:"registries"`
	}
	if !decode(w, r, &in) {
		return
	}
	sources := in.Registries
	if sources == nil && strings.TrimSpace(in.Registry) != "" {
		sources = []catalogSource{{Name: "MCP Registry", URL: in.Registry}}
	}
	sources, e := normalizeCatalogSources(sources)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	// Persist valid configuration even if one upstream is temporarily unavailable.
	results := a.queryCatalogSources(r.Context(), sources, "", nil, true)
	raw, _ := json.Marshal(sources)
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, 503, "保存目录仓库失败")
		return
	}
	defer tx.Rollback(r.Context())
	for key, value := range map[string]string{"catalog_registries": string(raw), "catalog_registry": sources[0].URL} {
		if _, e = tx.Exec(r.Context(), "INSERT INTO system_settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); e != nil {
			fail(w, 503, "保存目录仓库失败")
			return
		}
	}
	if e = tx.Commit(r.Context()); e != nil {
		fail(w, 503, "保存目录仓库失败")
		return
	}
	a.registryMu.Lock()
	a.catalogRegistrySources = sources
	a.catalogRegistryURL = sources[0].URL
	a.registryMu.Unlock()
	respond(w, 200, map[string]any{"registry": sources[0].URL, "registries": sources, "sources": results})
}

var packagePattern = regexp.MustCompile(`^(@[a-zA-Z0-9_.-]+/)?[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?([a-zA-Z0-9.+-]*)$`)

func (a *App) install(w http.ResponseWriter, r *http.Request) {
	a.installOnline(w, r)
}
