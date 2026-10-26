package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type scanSource struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

func knownSources() []scanSource {
	home, _ := os.UserHomeDir()
	return []scanSource{
		{ID: "codex", Name: "Codex", Path: filepath.Join(home, ".codex", "config.toml"), Format: "toml"},
		{ID: "claude", Name: "Claude Code", Path: filepath.Join(home, ".claude.json"), Format: "json"},
		{ID: "cursor", Name: "Cursor", Path: filepath.Join(home, ".cursor", "mcp.json"), Format: "json"},
		{ID: "toolport", Name: "Toolport", Path: filepath.Join(home, "Library", "Application Support", "Toolport", "registry.json"), Format: "json"},
		{ID: "qoder-directory", Name: "Qoder 配置目录", Path: filepath.Join(home, ".qoder-cn"), Format: "directory"},
		{ID: "qoder", Name: "Qoder", Path: filepath.Join(home, ".qoder-cn", "mcp.json"), Format: "json"},
		{ID: "qoder-shared", Name: "Qoder 共享", Path: filepath.Join(home, ".qoder-cn", "shared_client", "mcp.json"), Format: "json"},
	}
}
func (a *App) reloadScanRoots(ctx context.Context) error {
	rows, err := a.DB.Query(ctx, "SELECT path FROM scan_roots ORDER BY path")
	if err != nil {
		return err
	}
	defer rows.Close()
	roots := []string{}
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return err
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	a.scanRootsMu.Lock()
	a.savedScanRoots = roots
	a.scanRootsMu.Unlock()
	return nil
}
func (a *App) configuredScanRoots() []string {
	a.scanRootsMu.RLock()
	defer a.scanRootsMu.RUnlock()
	return append([]string(nil), a.savedScanRoots...)
}
func (a *App) scanRootsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		items := []map[string]any{}
		for _, root := range a.configuredScanRoots() {
			info, err := os.Stat(root)
			items = append(items, map[string]any{"path": root, "available": err == nil && info.IsDir(), "managed": true})
		}
		for _, root := range a.ScanRoots {
			if root == "" {
				continue
			}
			info, err := os.Stat(root)
			items = append(items, map[string]any{"path": root, "available": err == nil && info.IsDir(), "managed": false})
		}
		respond(w, 200, map[string]any{"items": items, "total": len(items)})
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &in) {
		return
	}
	path := strings.TrimSpace(in.Path)
	if path == "" || len(path) > 4096 || !filepath.IsAbs(path) {
		fail(w, 400, "请输入有效的绝对目录路径")
		return
	}
	if r.Method == http.MethodDelete {
		resolved := filepath.Clean(path)
		if canonical, err := filepath.EvalSymlinks(path); err == nil {
			resolved = canonical
		}
		result, err := a.DB.Exec(r.Context(), "DELETE FROM scan_roots WHERE path=$1", resolved)
		if err != nil {
			fail(w, 503, "移除扫描目录失败")
			return
		}
		if result.RowsAffected() == 0 {
			fail(w, 404, "目录不在页面配置中")
			return
		}
		if err := a.reloadScanRoots(r.Context()); err != nil {
			fail(w, 503, "重新加载扫描目录失败")
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		fail(w, 400, "目录不存在或不可读")
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() || resolved == string(filepath.Separator) {
		fail(w, 400, "请选择具体的可读配置目录")
		return
	}
	if _, err := os.ReadDir(resolved); err != nil {
		fail(w, 400, "目录不可读")
		return
	}
	if r.Method == http.MethodPost {
		if len(a.configuredScanRoots()) >= 20 {
			fail(w, 400, "最多添加20个扫描目录")
			return
		}
		result, err := a.DB.Exec(r.Context(), "INSERT OR IGNORE INTO scan_roots(path) VALUES($1)", resolved)
		if err != nil {
			fail(w, 503, "保存扫描目录失败")
			return
		}
		if result.RowsAffected() == 0 {
			fail(w, 409, "目录已添加")
			return
		}
		if err := a.reloadScanRoots(r.Context()); err != nil {
			fail(w, 503, "重新加载扫描目录失败")
			return
		}
		respond(w, 201, map[string]any{"path": resolved})
	}
}
func (a *App) scanPathAllowed(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, s := range knownSources() {
		if s.Format == "directory" && safePath([]string{s.Path}, abs) {
			return true
		}
		if abs == s.Path {
			resolved, e := filepath.EvalSymlinks(abs)
			return e == nil && resolved == abs
		}
	}
	if safePath(append(append([]string(nil), a.ScanRoots...), a.configuredScanRoots()...), abs) {
		return true
	}
	if a.LocalOnly {
		home, err := os.UserHomeDir()
		return err == nil && abs != home && safePath([]string{home}, abs)
	}
	return false
}

func isMCPConfigFile(name string) bool {
	name = strings.ToLower(name)
	return name == "config.toml" || name == "registry.json" ||
		(strings.Contains(name, "mcp") && (strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".toml")))
}

func (a *App) scanCandidates(path string) ([]string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, errors.New("来源不存在")
	}
	if !a.scanPathAllowed(path) {
		return nil, errors.New("来源不在允许读取的范围")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, errors.New("来源不可读")
	}
	if info.Mode().IsRegular() {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".json" && ext != ".toml" {
			return nil, errors.New("仅支持 JSON / TOML 配置文件")
		}
		return []string{path}, nil
	}
	if !info.IsDir() {
		return nil, errors.New("来源不是普通文件或目录")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, errors.New("目录不可读")
	}
	paths := []string{}
	visited := 0
	err = filepath.WalkDir(resolved, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > 2000 {
			return errors.New("目录文件过多，请选择更具体的目录")
		}
		rel, _ := filepath.Rel(resolved, candidate)
		depth := strings.Count(rel, string(filepath.Separator))
		if entry.IsDir() {
			if depth >= 3 || (candidate != resolved && (entry.Name() == "node_modules" || entry.Name() == ".git" || entry.Name() == "logs")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !isMCPConfigFile(entry.Name()) || !a.scanPathAllowed(candidate) {
			return nil
		}
		paths = append(paths, candidate)
		if len(paths) > 20 {
			return errors.New("目录中的 MCP 配置文件超过20个，请选择更具体的目录")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("目录中未找到 MCP 配置文件")
	}
	return paths, nil
}
func (a *App) scanSources(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "admin" {
		fail(w, 403, "仅管理员可查看扫描来源")
		return
	}
	sources := knownSources()
	listed := map[string]bool{}
	for _, source := range sources {
		listed[source.Path] = true
	}
	for _, root := range a.configuredScanRoots() {
		if listed[root] {
			continue
		}
		listed[root] = true
		sources = append(sources, scanSource{ID: "saved-" + hash(root)[:10], Name: "已添加目录", Path: root, Format: "directory"})
	}
	for n := range sources {
		if _, err := os.Stat(sources[n].Path); os.IsNotExist(err) {
			sources[n].Reason = "文件不存在"
			continue
		}
		if !a.scanPathAllowed(sources[n].Path) {
			sources[n].Reason = "不在允许读取的范围"
			continue
		}
		f, e := os.Open(sources[n].Path)
		if e != nil {
			sources[n].Reason = "文件不可读"
			continue
		}
		info, e := f.Stat()
		f.Close()
		if e != nil || (sources[n].Format == "directory" && !info.IsDir()) || (sources[n].Format != "directory" && !info.Mode().IsRegular()) {
			sources[n].Reason = "不是预期的配置文件或目录"
			continue
		}
		sources[n].Available = true
	}
	respond(w, 200, map[string]any{"items": sources, "total": len(sources)})
}
func (a *App) readScanSource(path string) ([]byte, error) {
	if !a.scanPathAllowed(path) {
		return nil, errors.New("来源不在允许读取的范围")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, errors.New("来源不可读")
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return nil, errors.New("来源不是普通文件")
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil {
		return nil, errors.New("读取失败")
	}
	if len(b) > 1<<20 {
		return nil, errors.New("来源超过1MiB限制")
	}
	return b, nil
}

type scannedInstance struct {
	Instance   Instance
	Warnings   []string
	Importable bool
}

func stringValue(v any) string { s, _ := v.(string); return s }
func stringMap(v any) map[string]string {
	out := map[string]string{}
	if m, ok := v.(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}
func normalizeScanned(name string, raw map[string]any) scannedInstance {
	i := Instance{Name: name, Command: stringValue(raw["command"]), URL: stringValue(raw["url"]), Cwd: stringValue(raw["cwd"]), Env: stringMap(raw["env"]), Headers: stringMap(raw["headers"])}
	for k, v := range stringMap(raw["http_headers"]) {
		i.Headers[k] = v
	}
	if args, ok := raw["args"].([]any); ok {
		for _, v := range args {
			if s, ok := v.(string); ok {
				i.Args = append(i.Args, s)
			}
		}
	}
	warnings := []string{}
	if helper := stringValue(raw["http_headers_helper"]); helper != "" {
		i.HeaderHelper = helper
	}
	resolve := func(key string) (string, bool) {
		v, ok := os.LookupEnv(key)
		if !ok || v == "" {
			warnings = append(warnings, "配置引用的环境变量不可用，请手动配置凭据")
			return "", false
		}
		return v, true
	}
	for header, key := range stringMap(raw["env_http_headers"]) {
		if v, ok := resolve(key); ok {
			i.Headers[header] = v
		}
	}
	if key := stringValue(raw["bearer_token_env_var"]); key != "" {
		if v, ok := resolve(key); ok {
			i.Headers["Authorization"] = "Bearer " + v
		}
	}
	if env, ok := raw["env"].([]any); ok {
		for _, entry := range env {
			if m, ok := entry.(map[string]any); ok {
				key := stringValue(m["key"])
				if value, ok := m["value"].(string); ok {
					i.Env[key] = value
				} else {
					warnings = append(warnings, "Toolport 环境凭据为外部引用，请手动补充")
				}
			}
		}
	}
	if i.URL != "" {
		i.Transport = "http"
	} else {
		i.Transport = "stdio"
	}
	if e := validate(&i); e != nil {
		warnings = append(warnings, "连接配置不完整或不受支持")
	}
	return scannedInstance{Instance: i, Warnings: warnings, Importable: len(warnings) == 0}
}
func parseScanSource(path string, b []byte) ([]scannedInstance, error) {
	var root map[string]any
	var e error
	if strings.EqualFold(filepath.Ext(path), ".toml") {
		e = toml.Unmarshal(b, &root)
	} else {
		e = json.Unmarshal(b, &root)
	}
	if e != nil {
		return nil, errors.New("配置格式无法解析")
	}
	out := []scannedInstance{}
	add := func(v any, prefix string) {
		if m, ok := v.(map[string]any); ok {
			for name, v := range m {
				if raw, ok := v.(map[string]any); ok {
					out = append(out, normalizeScanned(prefix+name, raw))
				}
			}
		}
	}
	add(root["mcp_servers"], "")
	add(root["mcpServers"], "")
	if servers, ok := root["servers"].([]any); ok {
		for _, v := range servers {
			if raw, ok := v.(map[string]any); ok {
				name := stringValue(raw["name"])
				if name == "" {
					name = stringValue(raw["id"])
				}
				out = append(out, normalizeScanned(name, raw))
			}
		}
	}
	// Claude may maintain project-scoped MCP entries in the same known file.
	if projects, ok := root["projects"].(map[string]any); ok {
		for path, v := range projects {
			if p, ok := v.(map[string]any); ok {
				add(p["mcpServers"], filepath.Base(path)+"-"+hash(path)[:10]+"/")
			}
		}
	}
	return out, nil
}
func (a *App) scan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Paths []string `json:"paths"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Paths) > 20 {
		fail(w, 400, "最多指定20个附加配置来源")
		return
	}
	// Extra paths extend the known clients; they never replace them.
	paths := make([]string, 0, len(in.Paths)+len(knownSources()))
	for _, source := range knownSources() {
		paths = append(paths, source.Path)
	}
	paths = append(paths, a.configuredScanRoots()...)
	paths = append(paths, in.Paths...)
	for n, p := range paths {
		abs, e := filepath.Abs(p)
		if e != nil {
			fail(w, 400, "无效来源路径")
			return
		}
		paths[n] = abs
	}
	a.task(w, r, "scan", func(ctx context.Context) (any, error) {
		total := 0
		sources := []map[string]any{}
		seen := map[string]bool{}
		for _, input := range paths {
			candidates, expandErr := a.scanCandidates(input)
			if expandErr != nil {
				sources = append(sources, map[string]any{"path": input, "status": "failed", "discovered": 0, "error": expandErr.Error()})
				continue
			}
			for _, p := range candidates {
				if seen[p] {
					continue
				}
				seen[p] = true
				result := map[string]any{"path": p, "status": "failed", "discovered": 0, "error": ""}
				b, e := a.readScanSource(p)
				if e != nil {
					result["error"] = e.Error()
					sources = append(sources, result)
					continue
				}
				entries, e := parseScanSource(p, b)
				if e != nil {
					result["error"] = e.Error()
					sources = append(sources, result)
					continue
				}
				fingerprint := hash(string(b))
				count := 0
				for _, entry := range entries {
					i := entry.Instance
					config, _ := json.Marshal(publicInstance(i))
					warnings, _ := json.Marshal(entry.Warnings)
					_, e = a.DB.Exec(ctx, `INSERT INTO discoveries(id,source,name,config,secret,fingerprint,warnings,importable) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(source,name) DO UPDATE SET config=excluded.config,secret=excluded.secret,fingerprint=excluded.fingerprint,warnings=excluded.warnings,importable=excluded.importable,scanned_at=CURRENT_TIMESTAMP`, ID(), p, i.Name, config, a.seal(instanceSecrets(i)), fingerprint, warnings, entry.Importable)
					if e != nil {
						result["error"] = "发现记录保存失败"
						break
					}
					count++
				}
				result["discovered"] = count
				total += count
				if e == nil {
					result["status"] = "succeeded"
				}
				sources = append(sources, result)
			}
		}
		return map[string]any{"discovered": total, "sources": sources, "note": "仅扫描配置，不执行助手或启动服务；纳管需管理员确认"}, nil
	})
}
func (a *App) discoveries(w http.ResponseWriter, r *http.Request) {
	if current(r).Role != "admin" {
		fail(w, 403, "扫描来源仅管理员可见")
		return
	}
	rows, e := a.DB.Query(r.Context(), "SELECT id,source,name,config,secret,fingerprint,warnings,importable,COALESCE(imported_instance_id,''),scanned_at FROM discoveries ORDER BY source,name")
	if e != nil {
		fail(w, 503, "查询失败")
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var id, source, name, fingerprint, imported string
		var scannedAt string
		var config, secret, warnings []byte
		var importable bool
		if rows.Scan(&id, &source, &name, &config, &secret, &fingerprint, &warnings, &importable, &imported, &scannedAt) != nil {
			fail(w, 503, "查询失败")
			return
		}
		var i Instance
		if err := json.Unmarshal(config, &i); err != nil {
			fail(w, 503, "发现记录无效")
			return
		}
		var sec secrets
		if len(secret) > 0 {
			var err error
			sec, err = a.unseal(secret)
			if err != nil {
				fail(w, 503, "发现凭据不可解密")
				return
			}
		}
		out = append(out, map[string]any{"id": id, "source": source, "name": name, "config": json.RawMessage(config), "preview": discoveryPreview(i, sec), "fingerprint": fingerprint, "warnings": json.RawMessage(warnings), "importable": importable, "imported_instance_id": imported, "scanned_at": scannedAt})
	}
	if rows.Err() != nil {
		fail(w, 503, "查询失败")
		return
	}
	respond(w, 200, map[string]any{"items": out, "total": len(out)})
}

func discoveryPreview(i Instance, sec secrets) map[string]any {
	args := make([]string, len(sec.Args))
	previousSensitive := false
	primaryShown := false
	for n, arg := range sec.Args {
		lower := strings.ToLower(arg)
		if previousSensitive {
			args[n] = "<REDACTED>"
			previousSensitive = false
			continue
		}
		if strings.Contains(lower, "://") || strings.HasPrefix(lower, "jdbc:") {
			args[n] = "<URL_REDACTED>"
			continue
		}
		if lower == "-h" || lower == "-e" || lower == "-k" || lower == "-c" || lower == "--env" {
			args[n] = arg
			previousSensitive = true
			continue
		}
		sensitive := strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "auth") || strings.Contains(lower, "cookie") || strings.Contains(lower, "header") || strings.Contains(lower, "key") || strings.Contains(lower, "passwd")
		if sensitive {
			if at := strings.IndexByte(arg, '='); at >= 0 {
				if strings.HasPrefix(arg, "-") {
					args[n] = arg[:at+1] + "<REDACTED>"
				} else {
					args[n] = "<REDACTED>"
				}
			} else {
				if strings.HasPrefix(arg, "-") && !strings.ContainsAny(arg, " \t:") {
					args[n] = arg
					previousSensitive = true
				} else {
					args[n] = "<REDACTED>"
				}
			}
			continue
		}
		if strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "ghp_") || strings.HasPrefix(lower, "eyj") {
			args[n] = "<REDACTED>"
			continue
		}
		if at := strings.IndexByte(arg, '='); at >= 0 {
			args[n] = arg[:at+1] + "<REDACTED>"
			continue
		}
		if strings.HasPrefix(arg, "-") {
			args[n] = arg
			switch lower {
			case "-y", "--yes", "--no-save", "--quiet", "-q", "--stdio", "--transport=stdio":
			default:
				previousSensitive = true
			}
			continue
		}
		// Only the first conventional package/script target is predictable from
		// npx/uvx/node/python invocation. Later positional values are arbitrary
		// application arguments and may be credentials.
		if !primaryShown && safeLaunchTarget(i.Command, arg) {
			args[n] = arg
			primaryShown = true
		} else {
			args[n] = "<REDACTED>"
		}
	}
	envKeys := make([]string, 0, len(sec.Env))
	for key := range sec.Env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	headerKeys := make([]string, 0, len(sec.Headers))
	for key := range sec.Headers {
		headerKeys = append(headerKeys, key)
	}
	sort.Strings(headerKeys)
	cwd := i.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	directory := map[string]any{"path": cwd, "exists": false}
	if info, err := os.Lstat(cwd); err == nil {
		directory["exists"] = true
		directory["is_directory"] = info.IsDir()
		directory["mode"] = info.Mode().String()
		directory["symlink"] = info.Mode()&os.ModeSymlink != 0
	}
	return map[string]any{"command": i.Command, "args": args, "cwd": cwd, "directory": directory, "env_keys": envKeys, "header_keys": headerKeys, "header_helper_configured": sec.HeaderHelper != "", "notice": "仅展示启动目录；进程仍可能访问服务账号可读取的其他目录"}
}
func safeLaunchTarget(command, arg string) bool {
	if arg == "" || len(arg) > 160 || strings.ContainsAny(arg, " \t\r\n:=\\") {
		return false
	}
	base := strings.ToLower(filepath.Base(command))
	if !(base == "npx" || base == "uvx" || base == "node" || base == "python" || base == "python3" || base == "npm") {
		return false
	}
	for _, r := range arg {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@._/-", r)) {
			return false
		}
	}
	return true
}
func (a *App) importDiscovery(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &in) {
		return
	}
	tx, e := a.DB.Begin(r.Context())
	if e != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	var source, fingerprint, imported string
	var config, secret []byte
	var importable bool
	e = tx.QueryRow(r.Context(), "SELECT source,config,secret,fingerprint,importable,COALESCE(imported_instance_id,'') FROM discoveries WHERE id=$1", r.PathValue("id")).Scan(&source, &config, &secret, &fingerprint, &importable, &imported)
	if e != nil {
		fail(w, 404, "发现记录不存在")
		return
	}
	if in.Fingerprint == "" || in.Fingerprint != fingerprint {
		fail(w, 409, "扫描版本变化，请重新扫描确认")
		return
	}
	if imported != "" {
		respond(w, 200, map[string]any{"id": imported, "instance_id": imported, "already_imported": true})
		return
	}
	b, e := a.readScanSource(source)
	if e != nil || hash(string(b)) != fingerprint {
		fail(w, 409, "来源变化或不可读，请重新扫描确认")
		return
	}
	if !importable {
		fail(w, 400, "来源含未解析凭据或不支持的配置，请手动配置实例")
		return
	}
	var i Instance
	if json.Unmarshal(config, &i) != nil {
		fail(w, 500, "发现配置无效")
		return
	}
	sec, e := a.unseal(secret)
	if e != nil {
		fail(w, 500, "发现凭据不可解密")
		return
	}
	i.Env = sec.Env
	i.Headers = sec.Headers
	i.Args = sec.Args
	if sec.URL != nil {
		i.URL = *sec.URL
	}
	if validate(&i) != nil {
		fail(w, 400, "实例配置无效")
		return
	}
	i.ID = ID()
	i.Version = 1
	i.Enabled = false
	i.Status = "stopped"
	config = configBytes(i)
	_, e = tx.Exec(r.Context(), "INSERT INTO instances(id,name,transport,config,secret) VALUES($1,$2,$3,$4,$5)", i.ID, i.Name, i.Transport, config, secret)
	if e == nil {
		_, e = tx.Exec(r.Context(), "INSERT INTO config_versions VALUES($1,1,$2,$3)", i.ID, config, secret)
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), "UPDATE discoveries SET imported_instance_id=$2 WHERE id=$1", r.PathValue("id"), i.ID)
	}
	if e != nil || tx.Commit(r.Context()) != nil {
		fail(w, 503, "纳管失败")
		return
	}
	taskID, startErr := a.enqueueTask(r.Context(), "import_start", func(ctx context.Context) (any, error) {
		return a.runImportedStarts(ctx, []string{i.ID})
	})
	respond(w, 201, map[string]any{"id": i.ID, "instance_id": i.ID, "already_imported": false, "start_task_id": taskID, "start_error": taskError(startErr)})
}

func (a *App) batchImportDiscoveries(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in struct {
		Items []struct {
			ID          string `json:"id"`
			Fingerprint string `json:"fingerprint"`
		} `json:"items"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Items) == 0 || len(in.Items) > 100 {
		fail(w, 400, "请选择1至100项待纳管配置")
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		fail(w, 503, "数据库不可用")
		return
	}
	defer tx.Rollback(r.Context())
	seen := map[string]bool{}
	sourceFingerprints := map[string]string{}
	importedIDs := []string{}
	for _, item := range in.Items {
		if item.ID == "" || item.Fingerprint == "" || seen[item.ID] {
			fail(w, 400, "选择项无效或重复")
			return
		}
		seen[item.ID] = true
		var source, fingerprint, imported string
		var config, secret []byte
		var importable bool
		err = tx.QueryRow(r.Context(), "SELECT source,config,secret,fingerprint,importable,COALESCE(imported_instance_id,'') FROM discoveries WHERE id=$1", item.ID).Scan(&source, &config, &secret, &fingerprint, &importable, &imported)
		if err != nil {
			fail(w, 404, "发现记录不存在，请刷新列表")
			return
		}
		if imported != "" {
			fail(w, 409, "选择项已纳管，请刷新列表")
			return
		}
		if !importable {
			fail(w, 400, "选择项需要补充凭据或配置，请先单独处理")
			return
		}
		if item.Fingerprint != fingerprint {
			fail(w, 409, "扫描结果已变化，请重新扫描")
			return
		}
		actual, checked := sourceFingerprints[source]
		if !checked {
			b, readErr := a.readScanSource(source)
			if readErr != nil {
				fail(w, 409, "来源不可读，请重新扫描")
				return
			}
			actual = hash(string(b))
			sourceFingerprints[source] = actual
		}
		if actual != fingerprint {
			fail(w, 409, "来源已变化，请重新扫描")
			return
		}
		var instance Instance
		if err = json.Unmarshal(config, &instance); err != nil {
			fail(w, 503, "发现配置无效")
			return
		}
		var credentials secrets
		credentials, err = a.unseal(secret)
		if err != nil {
			fail(w, 503, "发现凭据不可解密")
			return
		}
		instance.Env, instance.Headers, instance.Args = credentials.Env, credentials.Headers, credentials.Args
		if credentials.URL != nil {
			instance.URL = *credentials.URL
		}
		if err = validate(&instance); err != nil {
			fail(w, 400, "实例配置无效")
			return
		}
		instance.ID, instance.Version, instance.Enabled, instance.Status = ID(), 1, false, "stopped"
		public := configBytes(instance)
		if _, err = tx.Exec(r.Context(), "INSERT INTO instances(id,name,transport,config,secret) VALUES($1,$2,$3,$4,$5)", instance.ID, instance.Name, instance.Transport, public, secret); err == nil {
			_, err = tx.Exec(r.Context(), "INSERT INTO config_versions VALUES($1,1,$2,$3)", instance.ID, public, secret)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), "UPDATE discoveries SET imported_instance_id=$2 WHERE id=$1", item.ID, instance.ID)
		}
		if err != nil {
			fail(w, 503, "批量纳管失败，已回滚")
			return
		}
		importedIDs = append(importedIDs, instance.ID)
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, 503, "批量纳管失败，已回滚")
		return
	}
	taskID, startErr := a.enqueueTask(r.Context(), "import_start", func(ctx context.Context) (any, error) {
		return a.runImportedStarts(ctx, importedIDs)
	})
	respond(w, 201, map[string]any{"count": len(importedIDs), "instance_ids": importedIDs, "start_task_id": taskID, "start_error": taskError(startErr)})
}

func taskError(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) runImportedStarts(ctx context.Context, ids []string) (any, error) {
	type outcome struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	results := make([]outcome, len(ids))
	work := make(chan int)
	var group sync.WaitGroup
	for worker := 0; worker < 4 && worker < len(ids); worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range work {
				id := ids[index]
				results[index] = outcome{ID: id, Status: "fault"}
				l := a.lock(id)
				l.Lock()
				func() {
					defer l.Unlock()
					startCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
					defer cancel()
					instance, err := a.loadInstance(startCtx, id)
					if err != nil {
						return
					}
					if _, err := a.DB.Exec(startCtx, "UPDATE instances SET enabled=true,status='starting',last_error='' WHERE id=$1", id); err != nil {
						return
					}
					if err := a.start(startCtx, id); err != nil {
						detail := a.startupError(ctx, id, err)
						if !a.AllowStdio.Load() && (instance.Transport == "stdio" || instance.HeaderHelper != "") {
							detail = "本机命令运行未启用，请在系统设置中启用后重试"
						}
						faultCtx, faultCancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer faultCancel()
						a.DB.Exec(faultCtx, "UPDATE instances SET status='fault',last_error=$2 WHERE id=$1", id, detail)
						return
					}
					results[index].Status = "ready"
				}()
			}
		}()
	}
	for index := range ids {
		work <- index
	}
	close(work)
	group.Wait()
	ready := 0
	for _, result := range results {
		if result.Status == "ready" {
			ready++
		}
	}
	summary := map[string]any{"total": len(ids), "ready": ready, "failed": len(ids) - ready, "items": results}
	if ready != len(ids) {
		return summary, errors.New("部分实例连接失败，请在实例列表查看状态")
	}
	return summary, nil
}
