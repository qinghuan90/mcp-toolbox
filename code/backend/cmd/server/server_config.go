package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

type enterpriseConfig struct {
	ListenHost       string   `json:"listen_host"`
	ListenPort       int      `json:"listen_port"`
	DatabasePath     string   `json:"database_path"`
	PackageDirectory string   `json:"package_directory"`
	PublicURL        string   `json:"public_url"`
	AccessCIDRs      []string `json:"access_cidrs"`
	TLSCertFile      string   `json:"tls_cert_file"`
	TLSKeyFile       string   `json:"tls_key_file"`
}
type runtimeConfigStore struct {
	mu              sync.Mutex
	path            string
	current, active enterpriseConfig
	fingerprint     [32]byte
	overrides       map[string]string
}

var configEnvFields = map[string]string{"listen_host": "LISTEN_ADDR", "listen_port": "LISTEN_ADDR", "database_path": "SQLITE_PATH", "package_directory": "TOOLBOX_DESKTOP_RELEASES_DIR", "public_url": "PUBLIC_URL", "access_cidrs": "ACCESS_CIDRS", "tls_cert_file": "TLS_CERT_FILE", "tls_key_file": "TLS_KEY_FILE"}

func openEnterpriseConfig(path string) (*runtimeConfigStore, error) {
	if path == "" {
		path = os.Getenv("TOOLBOX_CONFIG_FILE")
	}
	if path == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(filepath.Dir(executable), "toolbox.json")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c := &runtimeConfigStore{path: abs, overrides: map[string]string{}}
	for field, key := range configEnvFields {
		if os.Getenv(key) != "" {
			c.overrides[field] = key
		}
	}
	raw, err := readEnterpriseConfig(abs)
	if errors.Is(err, os.ErrNotExist) {
		c.current = enterpriseConfig{ListenHost: "127.0.0.1", ListenPort: 8090, DatabasePath: "data/toolbox.sqlite", PackageDirectory: "packages", AccessCIDRs: []string{}}
		// Preserve a port selected in the old page before introducing this file.
		if db := os.Getenv("SQLITE_PATH"); db != "" {
			if p, e := readListenerPort(pathForListenerSettings(db)); e == nil && p != 0 {
				c.current.ListenPort = p
			}
		}
		if err = c.writeLocked(c.current, false); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		if c.current, err = decodeEnterpriseConfig(raw); err != nil {
			return nil, err
		}
		c.fingerprint = sha256.Sum256(raw)
	}
	if err = validateEnterpriseConfig(c.current); err != nil {
		return nil, err
	}
	c.active = c.current
	return c, nil
}
func readEnterpriseConfig(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("服务配置必须为不超过64KiB的普通文件")
	}
	return os.ReadFile(path)
}
func decodeEnterpriseConfig(raw []byte) (enterpriseConfig, error) {
	var c enterpriseConfig
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return c, errors.New("服务配置必须是JSON对象")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("服务配置格式错误：%w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("服务配置仅允许一个JSON对象")
	}
	if c.AccessCIDRs == nil {
		c.AccessCIDRs = []string{}
	}
	return c, validateEnterpriseConfig(c)
}
func validateEnterpriseConfig(c enterpriseConfig) error {
	if c.ListenPort < 1024 || c.ListenPort > 65535 {
		return errors.New("监听端口应为1024–65535")
	}
	ip := net.ParseIP(c.ListenHost)
	if ip == nil && c.ListenHost != "localhost" {
		return errors.New("监听地址必须为IP地址或localhost")
	}
	if strings.TrimSpace(c.DatabasePath) == "" || strings.TrimSpace(c.PackageDirectory) == "" {
		return errors.New("数据库和软件包保存路径不能为空")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return errors.New("TLS证书和私钥路径必须同时填写")
	}
	if c.PublicURL != "" {
		u, e := url.Parse(c.PublicURL)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("外部访问地址必须为有效HTTP或HTTPS地址")
		}
	}
	for _, value := range c.AccessCIDRs {
		_, network, e := net.ParseCIDR(value)
		if e != nil {
			return errors.New("访问来源必须为有效CIDR")
		}
		bits, _ := network.Mask.Size()
		if bits == 0 {
			return errors.New("访问来源不能包含全部地址")
		}
	}
	local := c.ListenHost == "localhost" || (ip != nil && ip.IsLoopback())
	if !local && (c.TLSCertFile == "" || len(c.AccessCIDRs) == 0 || !strings.HasPrefix(c.PublicURL, "https://")) {
		return errors.New("监听非本机地址需要TLS证书、HTTPS访问地址和访问来源CIDR")
	}
	return nil
}
func (c *runtimeConfigStore) resolved(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(filepath.Dir(c.path), path)
}
func (c *runtimeConfigStore) snapshot() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{"path": c.path, "values": c.current, "environment_overrides": c.overrides, "restart_required": !reflect.DeepEqual(c.current, c.active)}
}
func (c *runtimeConfigStore) writeLocked(next enterpriseConfig, check bool) error {
	if err := validateEnterpriseConfig(next); err != nil {
		return err
	}
	if check {
		raw, e := readEnterpriseConfig(c.path)
		if e != nil || sha256.Sum256(raw) != c.fingerprint {
			return errors.New("配置文件被外部修改，请重启服务后再保存")
		}
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := os.CreateTemp(filepath.Dir(c.path), ".toolbox-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), c.path); err != nil {
		return err
	}
	c.current = next
	c.fingerprint = sha256.Sum256(raw)
	return nil
}
func (c *runtimeConfigStore) save(raw json.RawMessage) error {
	next, err := decodeEnterpriseConfig(raw)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if next.PackageDirectory != c.current.PackageDirectory {
		return errors.New("软件包目录已变化，请刷新配置，并通过软件包管理修改目录")
	}
	old, _ := json.Marshal(c.current)
	var before, after map[string]any
	json.Unmarshal(old, &before)
	json.Unmarshal(raw, &after)
	for field, key := range c.overrides {
		if !reflect.DeepEqual(before[field], after[field]) {
			return fmt.Errorf("%s 当前由环境变量 %s 管理", field, key)
		}
	}
	return c.writeLocked(next, true)
}
func (c *runtimeConfigStore) packageDirectory() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if value := os.Getenv("TOOLBOX_DESKTOP_RELEASES_DIR"); value != "" {
		return value
	}
	return c.resolved(c.active.PackageDirectory)
}
func (c *runtimeConfigStore) changePackageDirectory(dir string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.overrides["package_directory"] != "" {
		return errors.New("软件包目录由环境变量管理")
	}
	next := c.current
	next.PackageDirectory = dir
	if err := c.writeLocked(next, true); err != nil {
		return err
	}
	c.active.PackageDirectory = dir
	return nil
}
func (c *runtimeConfigStore) changePort(port int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.current
	next.ListenPort = port
	if err := c.writeLocked(next, true); err != nil {
		return err
	}
	c.active.ListenPort = port
	return nil
}
func (c *runtimeConfigStore) startupEnv() error {
	for key, value := range map[string]string{"PUBLIC_URL": c.active.PublicURL, "ACCESS_CIDRS": strings.Join(c.active.AccessCIDRs, ","), "TLS_CERT_FILE": c.resolved(c.active.TLSCertFile), "TLS_KEY_FILE": c.resolved(c.active.TLSKeyFile)} {
		if value != "" && os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}
