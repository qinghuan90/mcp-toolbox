package platform

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const maxOfflineUpload int64 = 128 << 20
const maxOfflineExpanded int64 = 512 << 20

type offlineConfig struct {
	Name    string            `json:"name"`
	Runtime string            `json:"runtime"`
	Entry   string            `json:"entry"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func offlineRelative(name string) bool {
	return name != "" && !strings.ContainsAny(name, "\\\x00:") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}

// Extract only regular files; paths and expanded byte counts are checked before
// creating anything. Only owner permissions are retained; symlinks are rejected.
func extractOfflineZip(archive, dest string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return errors.New("安装包不是有效的 ZIP 文件")
	}
	defer z.Close()
	if len(z.File) == 0 || len(z.File) > 10000 {
		return errors.New("安装包文件数量须为1至10000")
	}
	var total int64
	seen := map[string]bool{}
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		if !offlineRelative(name) || seen[name] {
			return errors.New("安装包含不安全或重复的文件路径")
		}
		seen[name] = true
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return errors.New("安装包不支持符号链接或特殊文件")
		}
		if f.UncompressedSize64 > uint64(maxOfflineExpanded) || total > maxOfflineExpanded-int64(f.UncompressedSize64) {
			return errors.New("解压后文件超过512 MB")
		}
		total += int64(f.UncompressedSize64)
		target := filepath.Join(dest, filepath.FromSlash(name))
		if mode.IsDir() {
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		source, e := f.Open()
		if e != nil {
			return e
		}
		permission := os.FileMode(0600)
		if mode.Perm()&0111 != 0 {
			permission = 0700
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permission)
		if e != nil {
			source.Close()
			return e
		}
		n, e := io.Copy(out, io.LimitReader(source, int64(f.UncompressedSize64)+1))
		source.Close()
		closeErr := out.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if n != int64(f.UncompressedSize64) {
			return errors.New("安装包文件长度校验失败")
		}
	}
	return nil
}

// offlineArchiveFormat selects a parser from the declared extension. Each parser
// validates its own format; renaming a ZIP to TAR cannot bypass validation.
func offlineArchiveFormat(filename string) string {
	name := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(name, ".tar"):
		return "tar"
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	default:
		return ""
	}
}

func extractOfflineArchive(archive, dest, format string) error {
	switch format {
	case "zip":
		return extractOfflineZip(archive, dest)
	case "tar", "tar.gz":
		return extractOfflineTar(archive, dest, format == "tar.gz")
	default:
		return errors.New("仅支持 ZIP、TAR、TAR.GZ 或 TGZ 离线安装包")
	}
}

func extractOfflineTar(archive, dest string, compressed bool) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	var source io.Reader = file
	if compressed {
		gz, e := gzip.NewReader(file)
		if e != nil {
			return errors.New("安装包不是有效的 GZIP 文件")
		}
		defer gz.Close()
		source = gz
	}
	// Include a bounded allowance for TAR headers and padding, not just file data.
	expanded := &io.LimitedReader{R: source, N: maxOfflineExpanded + (16 << 20) + 1}
	tr := tar.NewReader(expanded)
	seen := map[string]bool{}
	var total int64
	count := 0
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return errors.New("安装包不是有效或完整的 TAR 文件")
		}
		count++
		if count > 10000 {
			return errors.New("安装包文件数量须为1至10000")
		}
		name := strings.TrimSuffix(header.Name, "/")
		// Standard tar commands commonly prefix every entry with ./.
		for strings.HasPrefix(name, "./") {
			name = strings.TrimPrefix(name, "./")
		}
		if name == "." && header.Typeflag == tar.TypeDir {
			continue
		}
		if !offlineRelative(name) || seen[name] {
			return errors.New("安装包含不安全或重复的文件路径")
		}
		seen[name] = true
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return errors.New("安装包不支持符号链接、硬链接或特殊文件")
		}
		if header.Size < 0 || header.Size > maxOfflineExpanded || total > maxOfflineExpanded-header.Size {
			return errors.New("解压后文件超过512 MB")
		}
		total += header.Size
		target := filepath.Join(dest, filepath.FromSlash(name))
		if header.Typeflag == tar.TypeDir {
			if header.Size != 0 {
				return errors.New("安装包目录长度无效")
			}
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		permission := os.FileMode(0600)
		if header.Mode&0111 != 0 {
			permission = 0700
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permission)
		if e != nil {
			return e
		}
		n, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil || n != header.Size {
			return errors.New("安装包文件长度校验失败")
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if count == 0 {
		return errors.New("安装包文件数量须为1至10000")
	}
	// Read through GZIP EOF to verify its CRC, and reject hidden trailing archives.
	buffer := make([]byte, 32768)
	for {
		n, e := expanded.Read(buffer)
		if expanded.N == 0 {
			return errors.New("解压后文件与元数据超过限制")
		}
		for _, value := range buffer[:n] {
			if value != 0 {
				return errors.New("安装包包含无效的尾部数据")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return errors.New("安装包校验失败或数据不完整")
		}
	}
	return nil
}

func offlineLaunch(root string, c offlineConfig) (Instance, error) {
	if c.Name == "" || len(c.Name) > 120 {
		return Instance{}, errors.New("请输入1至120字的实例名称")
	}
	if !offlineRelative(c.Entry) {
		return Instance{}, errors.New("入口必须是安装包内的相对文件路径")
	}
	entry := filepath.Join(root, filepath.FromSlash(c.Entry))
	info, err := os.Lstat(entry)
	if err != nil || !info.Mode().IsRegular() {
		return Instance{}, errors.New("入口文件不存在或不是普通文件")
	}
	if len(c.Args) > 100 || len(c.Env) > 100 {
		return Instance{}, errors.New("启动参数或环境变量过多")
	}
	for _, arg := range c.Args {
		if len(arg) > 8192 || strings.ContainsRune(arg, 0) {
			return Instance{}, errors.New("启动参数无效")
		}
	}
	for k, v := range c.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) || len(v) > 16384 {
			return Instance{}, errors.New("环境变量无效")
		}
	}
	var command string
	args := append([]string{}, c.Args...)
	switch c.Runtime {
	case "node", "python3":
		command, err = runtimeCommand(c.Runtime)
		if err != nil {
			return Instance{}, fmt.Errorf("服务器缺少 %s 运行环境，请安装后重试", c.Runtime)
		}
		command, err = filepath.Abs(command)
		if err != nil {
			return Instance{}, err
		}
		args = append([]string{entry}, args...)
	case "binary":
		command = entry
		if err = os.Chmod(entry, 0500); err != nil {
			return Instance{}, err
		}
	default:
		return Instance{}, errors.New("请选择 Node.js、Python 3 或原生可执行程序")
	}
	i := Instance{ID: ID(), Name: c.Name, Transport: "stdio", Command: command, Args: args, Env: c.Env, Cwd: root, Version: 1, Status: "stopped", Concurrency: 1, Timeout: 60}
	return i, validate(&i)
}

func (a *App) installOffline(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if !a.AllowStdio.Load() {
		fail(w, 409, "请先在系统与备份中启用本机 stdio 运行")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOfflineUpload+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, 400, "安装包上传失败，文件不能超过128 MB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	var config offlineConfig
	if err := json.Unmarshal([]byte(r.FormValue("config")), &config); err != nil {
		fail(w, 400, "安装配置格式无效")
		return
	}
	if config.Name == "" || len(config.Name) > 120 || !offlineRelative(config.Entry) || (config.Runtime != "node" && config.Runtime != "python3" && config.Runtime != "binary") {
		fail(w, 400, "请填写实例名称、运行方式和包内入口文件")
		return
	}
	source, header, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "请选择离线 ZIP、TAR 或 TAR.GZ 安装包")
		return
	}
	defer source.Close()
	format := offlineArchiveFormat(header.Filename)
	if format == "" {
		fail(w, 400, "仅支持包含完整依赖的 ZIP、TAR、TAR.GZ 或 TGZ 离线安装包")
		return
	}
	base := os.Getenv("ARTIFACT_DIR")
	if base == "" {
		base = filepath.Join(filepath.Dir(a.DB.Path), "artifacts")
	}
	base, err = filepath.Abs(base)
	if err != nil {
		fail(w, 503, "安装目录不可用")
		return
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		fail(w, 503, "无法创建安装目录")
		return
	}
	file, err := os.CreateTemp(base, ".offline-upload-")
	if err != nil {
		fail(w, 503, "无法暂存安装包")
		return
	}
	upload := file.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(upload)
		}
	}()
	digest := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(source, maxOfflineUpload+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || n == 0 || n > maxOfflineUpload {
		fail(w, 400, "安装包为空、超过128 MB或保存失败")
		return
	}
	checksum := hex.EncodeToString(digest.Sum(nil))
	taskID, err := a.enqueueTask(r.Context(), "offline_install", func(ctx context.Context) (any, error) {
		defer os.Remove(upload)
		return a.runOfflineInstall(ctx, base, upload, checksum, format, config)
	})
	if err != nil {
		fail(w, 503, err.Error())
		return
	}
	keep = true
	respond(w, 202, map[string]string{"id": taskID, "kind": "offline_install", "status": "queued"})
}

func (a *App) runOfflineInstall(ctx context.Context, base, upload, checksum, format string, config offlineConfig) (any, error) {
	result := map[string]any{"stage": "unpack", "sha256": checksum}
	failure := func(message string) (any, error) { result["message"] = message; return result, errors.New(message) }
	// Every installation owns its directory so server writes cannot modify a
	// second instance's installed code or dependencies.
	key := ID()
	final := filepath.Join(base, "offline-"+key)
	keepArtifact := false
	defer func() {
		if !keepArtifact {
			os.RemoveAll(final)
		}
	}()
	l := a.lock("artifact:" + key)
	l.Lock()
	defer l.Unlock()
	if _, err := os.Stat(final); os.IsNotExist(err) {
		temp, e := os.MkdirTemp(base, ".offline-extract-")
		if e != nil {
			return failure("无法创建解压目录")
		}
		defer os.RemoveAll(temp)
		if e = extractOfflineArchive(upload, temp, format); e != nil {
			return failure(e.Error())
		}
		if _, e = offlineLaunch(temp, config); e != nil {
			return failure(e.Error())
		}
		if e = os.Rename(temp, final); e != nil {
			return failure("无法发布离线安装目录")
		}
	} else if err != nil {
		return failure("安装目录不可用")
	}
	instance, err := offlineLaunch(final, config)
	if err != nil {
		return failure(err.Error())
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return failure("数据库不可用")
	}
	defer tx.Rollback(ctx)
	b, secret := configBytes(instance), a.seal(instanceSecrets(instance))
	_, err = tx.Exec(ctx, "INSERT INTO instances(id,name,transport,config,secret) VALUES($1,$2,$3,$4,$5)", instance.ID, instance.Name, instance.Transport, b, secret)
	if err == nil {
		_, err = tx.Exec(ctx, "INSERT INTO config_versions VALUES($1,1,$2,$3)", instance.ID, b, secret)
	}
	if err != nil {
		return failure("保存实例失败，本次安装文件已清理，可重试")
	}
	if err = tx.Commit(ctx); err != nil {
		keepArtifact = true
		return failure("保存结果未确认，安装文件已保留，请检查实例列表后重试")
	}
	keepArtifact = true
	result["instance_id"], result["stage"] = instance.ID, "connect"
	started, startErr := a.runImportedStarts(ctx, []string{instance.ID})
	result["connection"] = started
	if startErr != nil {
		return failure("离线包已安装，实例连接失败；请在 MCP 管理中补齐配置或重试")
	}
	result["stage"], result["message"] = "ready", "离线包已安装，实例已启动并完成工具发现"
	return result, nil
}
