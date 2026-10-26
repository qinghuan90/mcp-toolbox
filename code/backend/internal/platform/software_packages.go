package platform

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxSoftwarePackage = 512 << 20

var softwarePackageMu sync.Mutex
var packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
var packageVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,63}$`)

type SoftwarePackage struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	MinOS       string `json:"min_os"`
	Notes       string `json:"notes"`
	Filename    string `json:"filename"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Signature   string `json:"signature"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	PublishedAt string `json:"published_at,omitempty"`
	Integrity   string `json:"integrity"`
	URL         string `json:"url"`
}

func (a *App) packageDir() string {
	if a.PackageDirectory != nil {
		return a.PackageDirectory()
	}
	if a.DB != nil && a.DB.Path != "" {
		return filepath.Join(filepath.Dir(a.DB.Path), "packages")
	}
	executable, _ := os.Executable()
	return filepath.Join(filepath.Dir(executable), "packages")
}
func (a *App) packageAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !requireAdmin(w, r) {
		return false
	}
	if a.Personal {
		fail(w, 403, "软件包管理仅在企业版提供")
		return false
	}
	return true
}
func packageRoot(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}
func readPackageMetadata(root *os.Root) ([]SoftwarePackage, error) {
	out := []SoftwarePackage{}
	f, err := root.Open("index.json")
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out) > 10000 {
		return nil, errors.New("软件包索引过大")
	}
	seen := map[string]bool{}
	for _, p := range out {
		if !packageNamePattern.MatchString(p.Filename) || !packageNamePattern.MatchString(p.ID) || seen[p.ID] || !packageVersionPattern.MatchString(p.Version) || p.Size <= 0 || p.Size > maxSoftwarePackage || len(p.SHA256) != 64 {
			return nil, errors.New("软件包索引无效")
		}
		seen[p.ID] = true
	}
	return out, nil
}
func writePackageMetadata(root *os.Root, items []SoftwarePackage) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	name := ".index-" + randomPackageID()
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = root.Rename(name, "index.json"); err != nil {
		return err
	}
	// The rename committed the index. Directory sync is best effort because
	// some supported filesystems reject it; never report an uncommitted write
	// and delete its artifact after the committed index already references it.
	d, openErr := root.Open(".")
	if openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
func randomPackageID() string { return fmt.Sprintf("%d-%s", time.Now().UnixNano(), ID()) }
func verifyPackage(root *os.Root, p SoftwarePackage) bool {
	info, err := root.Lstat(p.Filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() != p.Size {
		return false
	}
	f, err := root.Open(p.Filename)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != p.Size {
		return false
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	return err == nil && hex.EncodeToString(h.Sum(nil)) == p.SHA256
}
func executableMatches(r io.ReaderAt, osName, arch string) bool {
	switch osName {
	case "windows":
		f, e := pe.NewFile(r)
		if e != nil {
			return false
		}
		defer f.Close()
		if f.OptionalHeader == nil || f.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 || f.Characteristics&pe.IMAGE_FILE_DLL != 0 {
			return false
		}
		return (arch == "amd64" && f.Machine == pe.IMAGE_FILE_MACHINE_AMD64) || (arch == "arm64" && f.Machine == pe.IMAGE_FILE_MACHINE_ARM64)
	case "darwin":
		f, e := macho.NewFile(r)
		if e != nil {
			return false
		}
		defer f.Close()
		return f.Type == macho.TypeExec && ((arch == "amd64" && f.Cpu == macho.CpuAmd64) || (arch == "arm64" && f.Cpu == macho.CpuArm64))
	case "linux":
		f, e := elf.NewFile(r)
		if e != nil {
			return false
		}
		defer f.Close()
		return (f.Type == elf.ET_EXEC || f.Type == elf.ET_DYN) && ((arch == "amd64" && f.Machine == elf.EM_X86_64) || (arch == "arm64" && f.Machine == elf.EM_AARCH64))
	}
	return false
}
func validatePackageBinary(f *os.File, size int64, ext, osName, arch string) error {
	if ext == ".exe" {
		if osName != "windows" || !executableMatches(f, osName, arch) {
			return errors.New("EXE 架构或平台与声明不符")
		}
		return nil
	}
	if ext != ".zip" {
		return errors.New("仅支持 ZIP 或 Windows EXE 安装包")
	}
	z, err := zip.NewReader(f, size)
	if err != nil {
		return errors.New("不是有效 ZIP 安装包")
	}
	if len(z.File) > 20000 {
		return errors.New("ZIP 文件数量过多")
	}
	var total uint64
	found := false
	for _, entry := range z.File {
		name := entry.Name
		clean := path.Clean(name)
		if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains("/"+name, "/../") || strings.Contains(name, ":") || entry.Mode()&os.ModeSymlink != 0 {
			return errors.New("ZIP 包含不安全路径或符号链接")
		}
		if entry.UncompressedSize64 > 1<<30 || total > (1<<30)-entry.UncompressedSize64 {
			return errors.New("ZIP 解压体积超过限制")
		}
		total += entry.UncompressedSize64
		candidate := osName == "windows" && strings.HasSuffix(strings.ToLower(name), ".exe") || osName == "darwin" && strings.Contains(name, ".app/Contents/MacOS/") || osName == "linux" && entry.Mode()&0111 != 0
		rc, e := entry.Open()
		if e != nil {
			return e
		}
		if !candidate || entry.UncompressedSize64 > 128<<20 {
			_, e = io.Copy(io.Discard, io.LimitReader(rc, (1<<30)+1))
			rc.Close()
			if e != nil {
				return errors.New("ZIP 文件内容校验失败")
			}
			continue
		}
		data, e := io.ReadAll(io.LimitReader(rc, (128<<20)+1))
		rc.Close()
		if e != nil {
			return e
		}
		if executableMatches(bytes.NewReader(data), osName, arch) {
			found = true
		}
	}
	if !found {
		return errors.New("ZIP 中未找到与目标平台和架构匹配的可执行程序")
	}
	return nil
}
func (a *App) softwarePackages(w http.ResponseWriter, r *http.Request) {
	if !a.packageAdmin(w, r) {
		return
	}
	softwarePackageMu.Lock()
	defer softwarePackageMu.Unlock()
	root, err := packageRoot(a.packageDir())
	if err != nil {
		fail(w, 503, "软件包目录不可用")
		return
	}
	defer root.Close()
	items, err := readPackageMetadata(root)
	if err != nil {
		fail(w, 503, "软件包索引不可读")
		return
	}
	if r.Method == "GET" {
		for i := range items {
			items[i].Integrity = "failed"
			if verifyPackage(root, items[i]) {
				items[i].Integrity = "verified"
			}
		}
		respond(w, 200, map[string]any{"items": items, "total": len(items)})
		return
	}
	if r.Method == "POST" {
		a.uploadSoftwarePackage(w, r, root, items)
		return
	}
	target := r.PathValue("id")
	for i := range items {
		if items[i].ID != target {
			continue
		}
		state := "archived"
		if r.Method == "PATCH" {
			var body struct {
				Status string `json:"status"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) != nil {
				fail(w, 400, "无效请求")
				return
			}
			state = body.Status
		}
		if state != "published" && state != "draft" && state != "archived" {
			fail(w, 400, "无效发布状态")
			return
		}
		if state == "published" && !verifyPackage(root, items[i]) {
			fail(w, 409, "安装包摘要校验失败，无法发布")
			return
		}
		items[i].Status = state
		if state == "published" {
			items[i].PublishedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if err = writePackageMetadata(root, items); err != nil {
			fail(w, 500, "保存发布状态失败")
			return
		}
		respond(w, 200, items[i])
		return
	}
	fail(w, 404, "软件包不存在")
}
func (a *App) uploadSoftwarePackage(w http.ResponseWriter, r *http.Request, root *os.Root, items []SoftwarePackage) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSoftwarePackage+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "请上传 multipart 软件包")
		return
	}
	fields := map[string]string{}
	var size int64
	var digest, ext string
	temp := ".upload-" + randomPackageID()
	defer root.Remove(temp)
	hasFile := false
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			fail(w, 400, "上传不完整或超过 512 MiB")
			return
		}
		if part.FormName() == "file" {
			if hasFile || part.FileName() == "" {
				fail(w, 400, "仅允许一个安装包")
				return
			}
			hasFile = true
			raw := part.Header.Get("Content-Disposition")
			_, disposition, parseErr := mime.ParseMediaType(raw)
			originalName := disposition["filename"]
			if parseErr != nil || strings.ContainsAny(originalName, `/\`) || strings.Contains(originalName, ":") {
				fail(w, 400, "文件名不安全")
				return
			}
			ext = strings.ToLower(filepath.Ext(part.FileName()))
			if ext != ".exe" && ext != ".zip" {
				fail(w, 400, "仅支持 ZIP 或 EXE")
				return
			}
			f, e := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				fail(w, 500, "无法写入软件包")
				return
			}
			h := sha256.New()
			size, e = io.Copy(io.MultiWriter(f, h), io.LimitReader(part, maxSoftwarePackage+1))
			if e == nil {
				e = f.Sync()
			}
			ce := f.Close()
			if e != nil || ce != nil || size <= 0 || size > maxSoftwarePackage {
				fail(w, 400, "安装包为空、过大或写入失败")
				return
			}
			digest = hex.EncodeToString(h.Sum(nil))
		} else {
			key := part.FormName()
			if key != "version" && key != "os" && key != "arch" && key != "min_os" && key != "notes" {
				fail(w, 400, "未知上传字段")
				return
			}
			if _, ok := fields[key]; ok {
				fail(w, 400, "重复上传字段")
				return
			}
			b, e := io.ReadAll(io.LimitReader(part, 16385))
			if e != nil || len(b) > 16384 {
				fail(w, 400, "字段过长")
				return
			}
			fields[key] = strings.TrimSpace(string(b))
		}
		part.Close()
	}
	version, osName, arch := fields["version"], fields["os"], fields["arch"]
	if !hasFile || !packageVersionPattern.MatchString(version) || (osName != "windows" && osName != "darwin" && osName != "linux") || (arch != "amd64" && arch != "arm64") {
		fail(w, 400, "版本、目标平台、架构或安装包无效")
		return
	}
	if osName == "windows" && fields["min_os"] != "11" {
		fail(w, 400, "Windows 安装包最低系统版本请填写 11")
		return
	}
	for _, p := range items {
		if p.Version == version && p.OS == osName && p.Arch == arch {
			fail(w, 409, "相同平台、架构和版本的软件包已存在（含归档）")
			return
		}
	}
	f, e := root.Open(temp)
	if e != nil {
		fail(w, 500, "无法读取上传文件")
		return
	}
	e = validatePackageBinary(f, size, ext, osName, arch)
	f.Close()
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	p := SoftwarePackage{ID: randomPackageID(), Version: version, OS: osName, Arch: arch, MinOS: fields["min_os"], Notes: fields["notes"], Size: size, SHA256: digest, Signature: "unsigned", Status: "draft", CreatedAt: time.Now().UTC().Format(time.RFC3339), Integrity: "verified"}
	p.Filename = "Toolbox-" + version + "-" + osName + "-" + arch + "-" + p.ID + ext
	p.URL = "/downloads/desktop/" + p.Filename
	if !packageNamePattern.MatchString(p.Filename) {
		fail(w, 400, "文件名过长")
		return
	}
	if e = root.Rename(temp, p.Filename); e != nil {
		fail(w, 500, "软件包提交失败")
		return
	}
	if e = writePackageMetadata(root, append(items, p)); e != nil {
		root.Remove(p.Filename)
		fail(w, 500, "软件包索引提交失败")
		return
	}
	respond(w, 201, p)
}
func (a *App) softwarePackageSettings(w http.ResponseWriter, r *http.Request) {
	if !a.packageAdmin(w, r) {
		return
	}
	softwarePackageMu.Lock()
	defer softwarePackageMu.Unlock()
	if r.Method == "GET" {
		respond(w, 200, map[string]any{"directory": a.packageDir(), "editable": a.PackageDirectoryEditable})
		return
	}
	if !a.PackageDirectoryEditable || a.ChangePackageDirectory == nil {
		fail(w, 409, "当前启动配置不允许在页面修改软件包目录")
		return
	}
	var body struct {
		Directory string `json:"directory"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body) != nil || !filepath.IsAbs(body.Directory) {
		fail(w, 400, "请填写绝对目录")
		return
	}
	body.Directory = filepath.Clean(body.Directory)
	if body.Directory != filepath.Clean(a.packageDir()) {
		root, e := packageRoot(a.packageDir())
		if e != nil {
			fail(w, 503, "原软件包目录不可用，无法安全切换")
			return
		}
		items, e := readPackageMetadata(root)
		_, manifestErr := root.Lstat("manifest.json")
		root.Close()
		if e != nil || len(items) > 0 || manifestErr == nil {
			fail(w, 409, "当前目录已有软件包，请先迁移完整目录并通过服务器配置切换，避免丢失版本")
			return
		}
	}
	root, e := packageRoot(body.Directory)
	if e != nil {
		fail(w, 400, "目标目录不可写")
		return
	}
	if _, e = readPackageMetadata(root); e != nil {
		root.Close()
		fail(w, 400, "目标目录软件包索引不可读")
		return
	}
	probe := ".probe-" + randomPackageID()
	f, e := root.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e == nil {
		e = f.Close()
		root.Remove(probe)
	}
	root.Close()
	if e != nil {
		fail(w, 400, "目标目录不可写")
		return
	}
	if e = a.ChangePackageDirectory(body.Directory); e != nil {
		fail(w, 500, "软件包目录配置保存失败")
		return
	}
	respond(w, 200, map[string]any{"directory": a.packageDir(), "editable": true})
}
func sortSoftwarePackages(items []SoftwarePackage) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
}
