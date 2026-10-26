package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type downloadablePackage struct {
	Package   SoftwarePackage
	Directory string
}

func (a *App) publishedSoftwarePackages() ([]downloadablePackage, error) {
	out := []downloadablePackage{}
	managed := map[string]bool{}
	dir := a.packageDir()
	root, err := os.OpenRoot(dir)
	if err == nil {
		items, e := readPackageMetadata(root)
		if e != nil {
			root.Close()
			return nil, e
		}
		sortSoftwarePackages(items)
		for _, p := range items {
			managed[p.OS+"/"+p.Arch+"/"+p.Version] = true
			if p.Status == "published" && verifyPackage(root, p) {
				p.Integrity = "verified"
				p.URL = "/downloads/desktop/" + p.Filename
				out = append(out, downloadablePackage{p, dir})
			}
		}
		root.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Support distribution manifests from existing releases, without advertising an
	// unverified file or inheriting a publisher's untrusted signature assertion.
	legacy := os.Getenv("TOOLBOX_DESKTOP_RELEASES_DIR")
	if legacy == "" {
		legacy = dir
	}
	roots := []string{legacy}
	if os.Getenv("TOOLBOX_DESKTOP_RELEASES_DIR") == "" {
		if exe, e := os.Executable(); e == nil {
			old := filepath.Join(filepath.Dir(exe), "desktop")
			if old != legacy {
				roots = append(roots, old)
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range out {
		seen[p.Package.Filename] = true
	}
	for _, legacy = range roots {
		root, err = os.OpenRoot(legacy)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		f, e := root.Open("manifest.json")
		if os.IsNotExist(e) {
			root.Close()
			continue
		}
		if e != nil {
			root.Close()
			return nil, e
		}
		var manifest struct {
			Releases []SoftwarePackage `json:"releases"`
		}
		e = json.NewDecoder(io.LimitReader(f, 256<<10)).Decode(&manifest)
		f.Close()
		if e != nil {
			root.Close()
			return nil, e
		}
		if len(manifest.Releases) > 128 {
			root.Close()
			continue
		}
		for _, p := range manifest.Releases {
			if !packageNamePattern.MatchString(p.Filename) || !packageVersionPattern.MatchString(p.Version) || seen[p.Filename] || managed[p.OS+"/"+p.Arch+"/"+p.Version] || p.Size <= 0 || p.Size > maxSoftwarePackage || (p.OS != "darwin" && p.OS != "windows" && p.OS != "linux") || (p.Arch != "arm64" && p.Arch != "amd64") || !verifyPackage(root, p) {
				continue
			}
			p.Status = "published"
			p.Signature = "unsigned"
			p.Integrity = "verified"
			p.URL = "/downloads/desktop/" + p.Filename
			out = append(out, downloadablePackage{p, legacy})
			seen[p.Filename] = true
		}
		root.Close()
	}
	return out, nil
}
func (a *App) desktopReleases(w http.ResponseWriter, r *http.Request) {
	softwarePackageMu.Lock()
	items, err := a.publishedSoftwarePackages()
	softwarePackageMu.Unlock()
	if err != nil {
		fail(w, 503, "桌面安装包目录暂时不可用")
		return
	}
	releases := []SoftwarePackage{}
	for _, p := range items {
		if osFilter := r.URL.Query().Get("os"); osFilter != "" && osFilter != p.Package.OS {
			continue
		}
		if arch := r.URL.Query().Get("arch"); arch != "" && arch != p.Package.Arch {
			continue
		}
		releases = append(releases, p.Package)
	}
	respond(w, 200, map[string]any{"items": releases, "total": len(releases)})
}
func (a *App) desktopDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("filename")
	if !packageNamePattern.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	softwarePackageMu.Lock()
	items, err := a.publishedSoftwarePackages()
	softwarePackageMu.Unlock()
	if err != nil {
		fail(w, 503, "安装包暂时不可用")
		return
	}
	for _, item := range items {
		if item.Package.Filename != name {
			continue
		}
		root, e := os.OpenRoot(item.Directory)
		if e != nil {
			fail(w, 503, "安装包目录不可用")
			return
		}
		defer root.Close()
		f, e := root.Open(name)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() != item.Package.Size {
			fail(w, 503, "安装包校验失败")
			return
		}
		// Revalidate the exact descriptor sent to the caller; a path may have changed
		// since release enumeration.
		h := sha256.New()
		if _, e = io.Copy(h, f); e != nil || hex.EncodeToString(h.Sum(nil)) != item.Package.SHA256 {
			fail(w, 503, "安装包摘要校验失败")
			return
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			fail(w, 503, "安装包读取失败")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", `"`+item.Package.SHA256+`"`)
		w.Header().Set("Cache-Control", "private, no-cache")
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}
	http.NotFound(w, r)
}
