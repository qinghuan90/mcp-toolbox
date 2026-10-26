package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type onlineTaskKey struct{}

type onlineConfig struct {
	Ecosystem string            `json:"ecosystem"`
	Package   string            `json:"package"`
	Version   string            `json:"version"`
	Name      string            `json:"name"`
	Entry     string            `json:"entry"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
}

func (a *App) installOnline(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var c onlineConfig
	if !decode(w, r, &c) {
		return
	}
	if !packagePattern.MatchString(c.Package) || !versionPattern.MatchString(c.Version) || (c.Ecosystem != "npm" && c.Ecosystem != "pypi") || (c.Ecosystem == "pypi" && strings.Contains(c.Package, "/")) {
		fail(w, 400, "需要 npm/PyPI、有效包名和固定版本")
		return
	}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len(c.Name) > 120 {
		fail(w, 400, "请输入1至120字的实例名称")
		return
	}
	if len(c.Args) > 100 || len(c.Env) > 100 {
		fail(w, 400, "启动参数或环境变量过多")
		return
	}
	for _, v := range c.Args {
		if len(v) > 8192 || strings.ContainsRune(v, 0) {
			fail(w, 400, "启动参数无效")
			return
		}
	}
	for k, v := range c.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) || len(v) > 16384 {
			fail(w, 400, "环境变量无效")
			return
		}
	}
	if !a.AllowStdio.Load() {
		fail(w, 409, "请先在系统与备份中启用本机 stdio 运行")
		return
	}
	taskID := make(chan string, 1)
	id, err := a.enqueueTask(r.Context(), "install", func(ctx context.Context) (any, error) {
		return a.runOnlineInstall(context.WithValue(ctx, onlineTaskKey{}, <-taskID), c)
	})
	if err != nil {
		fail(w, 429, err.Error())
		return
	}
	taskID <- id
	respond(w, 202, map[string]string{"id": id, "kind": "install", "status": "queued"})
}

func chooseOnlineEntry(entries map[string]string, requested string) (string, error) {
	if requested != "" {
		if target, ok := entries[requested]; ok {
			return target, nil
		}
		return "", errors.New("所选可执行入口不存在，请从包提供的入口中选择")
	}
	if len(entries) == 1 {
		for _, target := range entries {
			return target, nil
		}
	}
	if len(entries) == 0 {
		return "", errors.New("包没有提供可执行入口，无法作为 MCP Server 启动")
	}
	return "", errors.New("包提供多个入口，请选择一个可执行入口后重试")
}

func npmOnlineEntries(root, pkg string) (map[string]string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	packageRoot := filepath.Join(root, "node_modules", filepath.FromSlash(pkg))
	b, e := os.ReadFile(filepath.Join(packageRoot, "package.json"))
	if e != nil {
		return nil, e
	}
	var metadata struct {
		Name string          `json:"name"`
		Bin  json.RawMessage `json:"bin"`
	}
	if e = json.Unmarshal(b, &metadata); e != nil {
		return nil, e
	}
	entries := map[string]string{}
	var single string
	if json.Unmarshal(metadata.Bin, &single) == nil && single != "" {
		entries[filepath.Base(pkg)] = single
	} else if len(metadata.Bin) > 0 && string(metadata.Bin) != "null" {
		if e = json.Unmarshal(metadata.Bin, &entries); e != nil {
			return nil, e
		}
	}
	for name, target := range entries {
		target = strings.TrimPrefix(target, "./")
		if !offlineRelative(target) {
			return nil, errors.New("包可执行入口包含不安全路径")
		}
		resolved, e := filepath.EvalSymlinks(filepath.Join(packageRoot, filepath.FromSlash(target)))
		if e != nil {
			return nil, e
		}
		relative, e := filepath.Rel(resolvedRoot, resolved)
		if e != nil || !offlineRelative(filepath.ToSlash(relative)) {
			return nil, errors.New("包可执行入口超出安装目录")
		}
		info, e := os.Stat(resolved)
		if e != nil || !info.Mode().IsRegular() {
			return nil, errors.New("包可执行入口不是普通文件")
		}
		entries[name] = resolved
	}
	return entries, nil
}

func (a *App) runOnlineInstall(ctx context.Context, c onlineConfig) (any, error) {
	result := map[string]any{"stage": "download", "package": c.Package, "version": c.Version}
	progress := func(stage, text string) {
		result["stage"], result["message"] = stage, text
		if id, ok := ctx.Value(onlineTaskKey{}).(string); ok {
			raw, _ := json.Marshal(result)
			a.DB.Exec(ctx, "UPDATE tasks SET result=$2 WHERE id=$1 AND status='running'", id, raw)
		}
	}
	progress("download", "正在下载固定版本及安装依赖…")
	failure := func(message string) (any, error) { result["message"] = message; return result, errors.New(message) }
	base := os.Getenv("ARTIFACT_DIR")
	if base == "" {
		base = "./data/artifacts"
	}
	base, e := filepath.Abs(base)
	if e != nil {
		return failure("安装根目录不可用")
	}
	if e = os.MkdirAll(base, 0700); e != nil {
		return failure("无法创建安装根目录")
	}
	// Python console scripts embed absolute interpreter paths. Create the private
	// final directory first, and never share a mutable installation across instances.
	root, e := os.MkdirTemp(base, "online-")
	if e != nil {
		return failure("无法创建独立安装目录")
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(root)
		}
	}()
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "PYTHONNOUSERSITE=1", "PIP_DISABLE_PIP_VERSION_CHECK=1"}
		if err := preparePlatformCommand(cmd); err != nil {
			return err
		}
		// Package-manager output can contain private registry credentials. Do not
		// return it to the browser; expose the safe failing stage instead.
		return cmd.Run()
	}
	entries := map[string]string{}
	command := ""
	if c.Ecosystem == "npm" {
		if _, e = exec.LookPath("npm"); e != nil {
			return failure("服务器缺少 npm，请安装 Node.js/npm 后重试")
		}
		if e = run("npm", "install", "--ignore-scripts", "--no-audit", "--no-fund", "--save-exact", "--prefix", root, c.Package+"@"+c.Version); e != nil {
			return failure("npm 下载或依赖安装失败，请检查包版本、网络及磁盘；安装脚本不会执行")
		}
		progress("entry", "依赖已安装，正在解析可执行入口…")
		entries, e = npmOnlineEntries(root, c.Package)
		if e != nil {
			return failure("无法解析 npm 可执行入口：" + e.Error())
		}
		command, e = exec.LookPath("node")
		if e != nil {
			return failure("服务器缺少 Node.js 运行环境")
		}
	} else {
		if _, e = exec.LookPath("uv"); e != nil {
			return failure("服务器缺少 uv，请安装后重试")
		}
		venv := filepath.Join(root, "venv")
		if e = run("uv", "venv", venv); e != nil {
			return failure("Python 虚拟环境创建失败，请检查 Python/uv 运行环境")
		}
		python := virtualPython(venv)
		if e = run("uv", "pip", "install", "--python", python, "--only-binary=:all:", c.Package+"=="+c.Version); e != nil {
			return failure("PyPI 下载或依赖安装失败；仅支持 wheel 包，请检查固定版本、网络和运行环境")
		}
		progress("entry", "依赖已安装，正在解析可执行入口…")
		entries, e = pythonOnlineEntries(ctx, python, venv, c.Package, c.Entry)
		if e != nil {
			return failure(e.Error())
		}
		command = python

	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	result["entries"] = names
	entry, e := chooseOnlineEntry(entries, c.Entry)
	if e != nil {
		return failure(e.Error())
	}
	args := append([]string{}, c.Args...)
	if c.Ecosystem == "npm" {
		args = append([]string{entry}, args...)
	} else {
		command, args, e = pythonOnlineLaunch(command, entry, args)
		if e != nil {
			return failure(e.Error())
		}
	}
	command, e = filepath.Abs(command)
	if e != nil {
		return failure("无法确定可执行程序路径")
	}
	instance := Instance{ID: ID(), Name: c.Name, Transport: "stdio", Command: command, Args: args, Env: c.Env, Cwd: root, Version: 1, Status: "stopped", Concurrency: 1, Timeout: 60}
	if e = validate(&instance); e != nil {
		return failure("启动配置无效：" + e.Error())
	}
	progress("save", "正在保存实例及加密配置…")
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return failure("数据库不可用")
	}
	defer tx.Rollback(ctx)
	b, secret := configBytes(instance), a.seal(instanceSecrets(instance))
	_, e = tx.Exec(ctx, "INSERT INTO instances(id,name,transport,config,secret) VALUES($1,$2,$3,$4,$5)", instance.ID, instance.Name, instance.Transport, b, secret)
	if e == nil {
		_, e = tx.Exec(ctx, "INSERT INTO config_versions VALUES($1,1,$2,$3)", instance.ID, b, secret)
	}
	if e != nil {
		return failure("保存实例失败，本次安装文件已清理")
	}
	if e = tx.Commit(ctx); e != nil {
		keep = true
		return failure("保存结果未确认，安装目录已保留，请检查实例列表")
	}
	keep = true
	result["instance_id"] = instance.ID
	progress("connect", "已安装，正在启动并验证 MCP 初始化与工具发现…")
	started, startErr := a.runImportedStarts(ctx, []string{instance.ID})
	result["connection"] = started
	if startErr != nil {
		return failure("包已安装，但 MCP 初始化或工具发现失败；请前往实例补齐参数、凭据后重试")
	}
	result["stage"], result["message"] = "ready", fmt.Sprintf("%s@%s 已安装，实例已启动并完成工具发现", c.Package, c.Version)
	return result, nil
}

// Inspect only the selected distribution's own source files using AST. Discovery
// does not import or execute package modules, and never probes dependency files.
const pythonEntryDiscovery = `import ast,importlib.metadata,json,pathlib,re,sys
D=importlib.metadata.distribution(sys.argv[1])
identifier=re.compile(r'^[A-Za-z_][A-Za-z0-9_]*$')
result={'scripts':[x.name for x in D.entry_points if x.group=='console_scripts'],'modules':[],'objects':[],'owned_modules':[]}
for f in list(D.files or [])[:10000]:
 p=pathlib.PurePosixPath(str(f))
 if p.suffix!='.py' or any(x in ('..','.') for x in p.parts): continue
 parts=list(p.with_suffix('').parts)
 main=parts[-1]=='__main__'
 if parts[-1] in ('__main__','__init__'): parts=parts[:-1]
 if not parts or not all(identifier.fullmatch(x) for x in parts): continue
 module='.'.join(parts)
 path=pathlib.Path(D.locate_file(f))
 root=pathlib.Path(D.locate_file('')).resolve()
 try:
  path.resolve().relative_to(root)
  if path.stat().st_size>2097152: continue
  tree=ast.parse(path.read_text(encoding='utf-8'))
 except (ValueError,OSError,SyntaxError,UnicodeError): continue
 result['owned_modules'].append(module)
 if main: result['modules'].append(module)
 for node in tree.body:
  if isinstance(node,ast.If) and isinstance(node.test,ast.Compare) and isinstance(node.test.left,ast.Name) and node.test.left.id=='__name__' and any(isinstance(x,ast.Constant) and x.value=='__main__' for x in node.test.comparators): result['modules'].append(module)
  if isinstance(node,(ast.Assign,ast.AnnAssign)):
   value=node.value
   if isinstance(value,ast.Call):
    constructor=value.func
    if (isinstance(constructor,ast.Name) and constructor.id=='FastMCP') or (isinstance(constructor,ast.Attribute) and constructor.attr=='FastMCP'):
     targets=node.targets if isinstance(node,ast.Assign) else [node.target]
     for target in targets:
      if isinstance(target,ast.Name) and identifier.fullmatch(target.id): result['objects'].append(module+':'+target.id)
print(json.dumps(result))`

var pythonSymbol = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)
var pythonObject = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func pythonOnlineEntries(ctx context.Context, python, venv, pkg, requested string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, python, "-I", "-c", pythonEntryDiscovery, pkg)
	if err := preparePlatformCommand(cmd); err != nil {
		return nil, err
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("无法读取 Python 包的入口元数据及模块文件")
	}
	var metadata struct {
		Scripts []string `json:"scripts"`
		Modules []string `json:"modules"`
		Objects []string `json:"objects"`
		Owned   []string `json:"owned_modules"`
	}
	if json.Unmarshal(output, &metadata) != nil {
		return nil, errors.New("Python 入口元数据无效")
	}
	entries := map[string]string{}
	for _, name := range metadata.Scripts {
		if !offlineRelative(name) || strings.Contains(name, "/") {
			return nil, errors.New("Python 可执行入口包含不安全路径")
		}
		target := virtualScript(venv, name)
		info, e := os.Lstat(target)
		if e != nil || !info.Mode().IsRegular() {
			return nil, errors.New("Python console_scripts 文件缺失")
		}
		entries[name] = target
	}
	// Prefer declared console scripts. Otherwise present detected executable
	// modules and FastMCP objects; ambiguity requires an explicit user selection.
	if len(entries) == 0 {
		for _, module := range metadata.Modules {
			if pythonSymbol.MatchString(module) {
				entries["module:"+module] = "module:" + module
			}
		}
		for _, object := range metadata.Objects {
			parts := strings.Split(object, ":")
			if len(parts) == 2 && pythonSymbol.MatchString(parts[0]) && pythonObject.MatchString(parts[1]) {
				entries["object:"+object] = "object:" + object
			}
		}
	}
	if strings.HasPrefix(requested, "module:") || strings.HasPrefix(requested, "object:") {
		parts := strings.Split(requested, ":")
		if _, _, e := pythonOnlineLaunch(python, requested, nil); e != nil {
			return nil, e
		}
		owned := false
		for _, module := range metadata.Owned {
			if module == parts[1] {
				owned = true
				break
			}
		}
		if !owned {
			return nil, errors.New("指定模块不属于本次安装的 Python 包")
		}
		entries[requested] = requested
	}
	return entries, nil
}

const pythonObjectRunner = `import importlib,sys
module,name=sys.argv[1:3]
sys.argv=[module]+sys.argv[3:]
server=getattr(importlib.import_module(module),name)
server.run(transport='stdio')`

func pythonOnlineLaunch(python, entry string, args []string) (string, []string, error) {
	if strings.HasPrefix(entry, "module:") {
		module := strings.TrimPrefix(entry, "module:")
		if !pythonSymbol.MatchString(module) {
			return "", nil, errors.New("Python 模块入口格式无效，请填写 module:包名.模块")
		}
		return python, append([]string{"-I", "-m", module}, args...), nil
	}
	if strings.HasPrefix(entry, "object:") {
		parts := strings.Split(entry, ":")
		if len(parts) != 3 || !pythonSymbol.MatchString(parts[1]) || !pythonObject.MatchString(parts[2]) {
			return "", nil, errors.New("Python 对象入口格式无效，请填写 object:包名.模块:对象名")
		}
		return python, append([]string{"-I", "-c", pythonObjectRunner, parts[1], parts[2]}, args...), nil
	}
	return entry, args, nil
}
