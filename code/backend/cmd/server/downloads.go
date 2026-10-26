package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/gin-gonic/gin"
)

type desktopRelease struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
	URL       string `json:"url"`
}

var releaseFilename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
var releaseDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

func desktopReleaseDir() string {
	if configured := os.Getenv("TOOLBOX_DESKTOP_RELEASES_DIR"); configured != "" {
		return configured
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(executable), "desktop")
}
func readDesktopReleases(dir string) ([]desktopRelease, error) {
	out := []desktopRelease{}
	root, err := os.OpenRoot(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open("manifest.json")
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var manifest struct {
		Releases []desktopRelease `json:"releases"`
	}
	if err = json.NewDecoder(io.LimitReader(file, 256<<10)).Decode(&manifest); err != nil {
		return nil, err
	}
	if len(manifest.Releases) > 32 {
		return out, nil
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Releases {
		if !releaseFilename.MatchString(entry.Filename) || !releaseDigest.MatchString(entry.SHA256) || entry.Version == "" || len(entry.Version) > 64 || entry.Size <= 0 || seen[entry.Filename] {
			continue
		}
		if entry.OS != "darwin" && entry.OS != "linux" && entry.OS != "windows" {
			continue
		}
		if entry.Arch != "arm64" && entry.Arch != "amd64" {
			continue
		}
		info, err := root.Lstat(entry.Filename)
		if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			continue
		}
		entry.URL = "/downloads/desktop/" + entry.Filename
		out = append(out, entry)
		seen[entry.Filename] = true
	}
	return out, nil
}
func attachDesktopDownloads(router *gin.Engine) {
	router.GET("/api/v1/desktop/releases", func(c *gin.Context) {
		entries, err := readDesktopReleases(desktopReleaseDir())
		if err != nil {
			c.JSON(503, gin.H{"error": "桌面安装包目录暂时不可用"})
			return
		}
		c.JSON(200, gin.H{"items": entries, "total": len(entries)})
	})
	router.GET("/downloads/desktop/:filename", func(c *gin.Context) {
		name := c.Param("filename")
		if !releaseFilename.MatchString(name) {
			c.Status(404)
			return
		}
		dir := desktopReleaseDir()
		entries, err := readDesktopReleases(dir)
		if err != nil {
			c.Status(503)
			return
		}
		var entry *desktopRelease
		for i := range entries {
			if entries[i].Filename == name {
				entry = &entries[i]
				break
			}
		}
		if entry == nil {
			c.Status(404)
			return
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			c.Status(503)
			return
		}
		defer root.Close()
		file, err := root.Open(name)
		if err != nil {
			c.Status(404)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			c.Status(503)
			return
		}
		digest := sha256.New()
		if _, err = io.Copy(digest, file); err != nil || hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
			c.JSON(503, gin.H{"error": "安装包校验失败，请联系管理员重新发布"})
			return
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			c.Status(503)
			return
		}
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		c.Header("Content-Type", "application/octet-stream")
		c.Header("ETag", `"`+entry.SHA256+`"`)
		c.Header("Cache-Control", "private, no-cache")
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
	})
}
