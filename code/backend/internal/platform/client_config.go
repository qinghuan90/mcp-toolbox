package platform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const maxClientConfig = 2 << 20

var clientNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type clientTarget struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Relative  string `json:"relative_file"`
	Format    string `json:"format"`
}

func clientTargets() []clientTarget {
	home, _ := os.UserHomeDir()
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	return []clientTarget{
		{"codex", "Codex", codex, "config.toml", "toml"},
		{"qoder", "Qoder 桌面 / CLI", filepath.Join(home, ".qoder"), "settings.json", "json"},
		{"qoder-cn-cli", "Qoder CN 桌面 / CLI", filepath.Join(home, ".qoder-cn"), "settings.json", "json"},
		{"qoder-cn", "Qoder CN IDE", filepath.Join(home, ".qoder-cn"), "shared_client/mcp.json", "json"},
		{"qoder-cn-legacy", "Qoder CN IDE（旧版配置）", filepath.Join(home, ".qoder-cn"), "mcp.json", "json"},
	}
}
func resolveClientTarget(id, directory string) (clientTarget, string, error) {
	for _, t := range clientTargets() {
		if t.ID == id {
			if directory == "" {
				directory = t.Directory
			}
			if !filepath.IsAbs(directory) || len(directory) > 4096 {
				return t, "", errors.New("请输入配置目录的绝对路径")
			}
			target := filepath.Join(filepath.Clean(directory), filepath.FromSlash(t.Relative))
			// Resolve the existing ancestor so the preview shows the actual destination,
			// including dotfile symlinks. The apply step verifies it has not changed.
			ancestor := target
			var rest []string
			for {
				_, err := os.Lstat(ancestor)
				if err == nil {
					resolved, e := filepath.EvalSymlinks(ancestor)
					if e != nil {
						return t, "", errors.New("配置路径的符号链接不可用")
					}
					for n := len(rest) - 1; n >= 0; n-- {
						resolved = filepath.Join(resolved, rest[n])
					}
					return t, resolved, nil
				}
				if !os.IsNotExist(err) {
					return t, "", err
				}
				parent := filepath.Dir(ancestor)
				if parent == ancestor {
					return t, "", errors.New("配置目录不可用")
				}
				rest = append(rest, filepath.Base(ancestor))
				ancestor = parent
			}
		}
	}
	return clientTarget{}, "", errors.New("不支持的客户端")
}
func readClientFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return []byte{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxClientConfig {
		return nil, false, errors.New("配置必须为不超过 2 MiB 的普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxClientConfig+1))
	if len(raw) > maxClientConfig {
		return nil, false, errors.New("配置文件过大")
	}
	return raw, true, err
}
func fileFingerprint(raw []byte, exists bool) string {
	if !exists {
		return "missing"
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func entryFingerprint(entry map[string]any) string {
	if entry == nil {
		return "missing"
	}
	b, _ := json.Marshal(entry)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func decodeClientConfig(raw []byte, format string) (map[string]any, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return root, nil
	}
	if format == "toml" {
		if err := toml.Unmarshal(raw, &root); err != nil {
			return nil, errors.New("现有 TOML 格式错误，请先在客户端修正；未修改文件")
		}
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&root) != nil || root == nil {
			return nil, errors.New("现有 JSON 格式错误，请先在客户端修正；未修改文件")
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return nil, errors.New("JSON 文件包含多余内容；未修改文件")
		}
	}
	return root, nil
}
func clientServer(root map[string]any, format, name string) (map[string]any, error) {
	key := "mcpServers"
	if format == "toml" {
		key = "mcp_servers"
	}
	if root[key] == nil {
		return nil, nil
	}
	servers, ok := root[key].(map[string]any)
	if !ok {
		return nil, errors.New("现有 MCP 配置不是对象；未修改文件")
	}
	if servers[name] == nil {
		return nil, nil
	}
	entry, ok := servers[name].(map[string]any)
	if !ok {
		return nil, errors.New("同名 MCP 配置不是对象；请改用其他服务名称")
	}
	return entry, nil
}

// Edit only the selected server. Unrelated JSON fields survive semantically;
// TOML bytes, comments, models and account settings outside that server survive verbatim.
func editClientConfig(raw []byte, format, name string, entry map[string]any) ([]byte, map[string]any, error) {
	if !clientNamePattern.MatchString(name) {
		return nil, nil, errors.New("配置名称仅支持 1–64 位字母、数字、下划线或短横线")
	}
	root, err := decodeClientConfig(raw, format)
	if err != nil {
		return nil, nil, err
	}
	previous, err := clientServer(root, format, name)
	if err != nil {
		return nil, nil, err
	}
	key := "mcpServers"
	if format == "toml" {
		key = "mcp_servers"
	}
	servers, _ := root[key].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		root[key] = servers
	}
	if entry == nil {
		delete(servers, name)
	} else {
		servers[name] = entry
	}
	if format != "toml" {
		out, e := json.MarshalIndent(root, "", "  ")
		if len(out)+1 > maxClientConfig {
			return nil, nil, errors.New("写入后的配置超过 2 MiB 限制；原文件未修改")
		}
		return append(out, '\n'), previous, e
	}
	type header struct {
		offset int
		target bool
	}
	var headers []header
	var parser unstable.Parser
	parser.Reset(raw)
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind != unstable.Table && node.Kind != unstable.ArrayTable {
			continue
		}
		iter := node.Key()
		var keys []string
		offset := -1
		for iter.Next() {
			n := iter.Node()
			if offset < 0 {
				offset = int(n.Raw.Offset)
			}
			keys = append(keys, string(n.Data))
		}
		if offset < 0 {
			continue
		}
		start := bytes.LastIndexByte(raw[:offset], '\n') + 1
		headers = append(headers, header{start, len(keys) >= 2 && keys[0] == "mcp_servers" && keys[1] == name})
	}
	if parser.Error() != nil {
		return nil, nil, errors.New("TOML 解析失败；未修改文件")
	}
	out := append([]byte{}, raw...)
	for n := len(headers) - 1; n >= 0; n-- {
		if !headers[n].target {
			continue
		}
		end := len(raw)
		if n+1 < len(headers) {
			end = headers[n+1].offset
		}
		out = append(out[:headers[n].offset], out[end:]...)
	}
	if entry != nil {
		block, e := toml.Marshal(map[string]any{"mcp_servers": map[string]any{name: entry}})
		if e != nil {
			return nil, nil, e
		}
		block = bytes.TrimPrefix(block, []byte("[mcp_servers]\n"))
		out = append(out, '\n')
		out = append(out, block...)
	}
	check, err := decodeClientConfig(out, format)
	if m, ok := root[key].(map[string]any); ok && len(m) == 0 {
		delete(root, key)
	}
	if m, ok := check[key].(map[string]any); ok && len(m) == 0 {
		delete(check, key)
	}
	if err != nil || !reflect.DeepEqual(check, root) {
		return nil, nil, errors.New("此 TOML 使用内联或点号 MCP 定义，请改用标准 [mcp_servers.名称] 配置后重试；原文件未修改")
	}
	if len(out) > maxClientConfig {
		return nil, nil, errors.New("写入后的配置超过 2 MiB 限制；原文件未修改")
	}
	return out, previous, nil
}
func sameExceptClientEntry(before, after []byte, format, name string) bool {
	a, e := decodeClientConfig(before, format)
	if e != nil {
		return false
	}
	b, e := decodeClientConfig(after, format)
	if e != nil {
		return false
	}
	key := "mcpServers"
	if format == "toml" {
		key = "mcp_servers"
	}
	for _, v := range []map[string]any{a, b} {
		if m, ok := v[key].(map[string]any); ok {
			delete(m, name)
			if len(m) == 0 {
				delete(v, key)
			}
		}
	}
	return reflect.DeepEqual(a, b)
}
func clientEntry(format, url, secret string) map[string]any {
	entry := map[string]any{"url": url}
	headers := map[string]any{"Authorization": "Bearer " + secret}
	if format == "toml" {
		entry["http_headers"] = headers
		entry["enabled"] = true
	} else {
		entry["type"] = "http"
		entry["headers"] = headers
	}
	return entry
}
func clientServerNames(root map[string]any, format string) []string {
	key := "mcpServers"
	if format == "toml" {
		key = "mcp_servers"
	}
	m, _ := root[key].(map[string]any)
	names := []string{}
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
func atomicClientWrite(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".toolbox-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		defer dir.Close()
		_ = dir.Sync()
	}
	return nil
}
func privateClientBackup(path string, raw []byte) (string, error) {
	name := path + ".toolbox-backup-" + ID() + ".bak"
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	return name, nil
}

func normalizeClientNumbers(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	var convert func(any) any
	convert = func(v any) any {
		switch x := v.(type) {
		case json.Number:
			if n, e := x.Int64(); e == nil {
				return n
			}
			f, _ := x.Float64()
			return f
		case map[string]any:
			for k, v := range x {
				x[k] = convert(v)
			}
			return x
		case []any:
			for n, v := range x {
				x[n] = convert(v)
			}
			return x
		default:
			return v
		}
	}
	return convert(m).(map[string]any)
}
