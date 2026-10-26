package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"toolbox/internal/runlog"
	"toolbox/internal/storage"
)

type User struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	Role               string `json:"role"`
	Enabled            bool   `json:"enabled"`
	MustChangePassword bool   `json:"must_change_password"`
	Personal           bool   `json:"personal,omitempty"`
}
type Instance struct {
	diagnosticSecrets      func() []string
	OAuthRequired          bool              `json:"oauth_required,omitempty"`
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Transport              string            `json:"transport"`
	URL                    string            `json:"url"`
	Command                string            `json:"command"`
	Args                   []string          `json:"args"`
	Cwd                    string            `json:"cwd"`
	Description            string            `json:"description"`
	AccountLabel           string            `json:"account_label"`
	Concurrency            int               `json:"concurrency"`
	Timeout                int               `json:"timeout_seconds"`
	Version                int               `json:"version"`
	Enabled                bool              `json:"enabled"`
	Status                 string            `json:"status"`
	LastError              string            `json:"last_error"`
	Env                    map[string]string `json:"env,omitempty"`
	Headers                map[string]string `json:"headers,omitempty"`
	HeaderHelper           string            `json:"header_helper,omitempty"`
	HeaderHelperConfigured bool              `json:"header_helper_configured,omitempty"`
	EnvKeys                []string          `json:"env_keys"`
	HeaderKeys             []string          `json:"header_keys"`
	Runtime                *RuntimeInfo      `json:"runtime,omitempty"`
	Health                 *InstanceHealth   `json:"health,omitempty"`
}
type RuntimeInfo struct {
	PID          *int       `json:"pid"`
	StartedAt    *time.Time `json:"started_at"`
	Health       string     `json:"health"`
	RestartCount int        `json:"restart_count"`
}
type secrets struct {
	Args         []string
	URL          *string
	Env          map[string]string
	Headers      map[string]string
	HeaderHelper string
}
type App struct {
	oauthMu         sync.Mutex
	oauthPending    sync.Map
	oauthChallenges sync.Map
	oauthLocks      sync.Map
	oauthHTTPClient *http.Client
	oauthSecrets    sync.Map

	PackageDirectory         func() string
	PackageDirectoryEditable bool
	ChangePackageDirectory   func(string) error
	ServerConfigSnapshot     func() map[string]any
	SaveServerConfiguration  func(json.RawMessage) error

	RunLogs                *runlog.Store
	healthEnabled          atomic.Bool
	healthInterval         atomic.Int64
	healthOnce             sync.Once
	healthCancel           context.CancelFunc
	healthWG               sync.WaitGroup
	Personal               bool
	originMu               sync.RWMutex
	auditMu                sync.Mutex
	stdioMu                sync.Mutex
	auditBlocked           atomic.Bool
	auditLost              atomic.Bool
	DB                     *storage.DB
	maintenance            sync.RWMutex
	restoring              atomic.Bool
	restoreMu              sync.Mutex
	commitMu               sync.Mutex
	restoreFiles           map[string]stagedRestore
	gatewayMu              sync.Mutex
	gatewayHandler         http.Handler
	gatewayLeases          map[string]*gatewayLease
	gatewayStreams         map[string]context.CancelFunc
	Key                    cipher.AEAD
	SetupKey               string
	Origin                 string
	ListenerPort           func() int
	ChangePort             func(int) (string, error)
	PortEditable           bool
	Secure                 bool
	AllowStdio             atomic.Bool
	StdioForced            bool
	LocalOnly              bool
	ScanRoots              []string
	scanRootsMu            sync.RWMutex
	savedScanRoots         []string
	registryMu             sync.RWMutex
	catalogRegistryURL     string
	catalogRegistrySources []catalogSource
	instanceHealth         sync.Map
	Runtime                sync.Map
	Locks                  sync.Map
	global                 chan struct{}
	users                  sync.Map
	jobs                   chan func()
	rateMu                 sync.Mutex
	attempts               map[string][]time.Time
}

func (a *App) PublicOrigin() string {
	a.originMu.RLock()
	defer a.originMu.RUnlock()
	return a.Origin
}

func (a *App) SetPublicOrigin(origin string) {
	a.originMu.Lock()
	a.Origin = origin
	a.originMu.Unlock()
}

func New(db *storage.DB) (*App, error) {
	key, e := base64.StdEncoding.DecodeString(os.Getenv("MASTER_KEY"))
	if e != nil || len(key) != 32 {
		return nil, errors.New("MASTER_KEY must be base64 encoded 32 bytes")
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	a := &App{DB: db, Key: aead, SetupKey: os.Getenv("SETUP_KEY"), Origin: strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"), Secure: os.Getenv("COOKIE_SECURE") != "false", StdioForced: os.Getenv("ALLOW_UNSANDBOXED_STDIO") == "true", ScanRoots: strings.Split(os.Getenv("SCAN_ROOTS"), string(os.PathListSeparator)), global: make(chan struct{}, 20), jobs: make(chan func(), 32), attempts: map[string][]time.Time{}, restoreFiles: map[string]stagedRestore{}, gatewayLeases: map[string]*gatewayLease{}, gatewayStreams: map[string]context.CancelFunc{}}
	if len(a.SetupKey) < 16 {
		return nil, errors.New("SETUP_KEY must contain at least 16 characters")
	}
	if err := a.reloadScanRoots(context.Background()); err != nil {
		return nil, err
	}
	if err := a.reloadCatalogRegistry(context.Background()); err != nil {
		return nil, err
	}
	if err := a.reloadStdioSetting(context.Background()); err != nil {
		return nil, err
	}
	if err := a.reloadHealthSettings(context.Background()); err != nil {
		return nil, err
	}
	a.auditBlocked.Store(true)
	for range 2 {
		go func() {
			for f := range a.jobs {
				a.maintenance.RLock()
				f()
				a.maintenance.RUnlock()
			}
		}()
	}
	return a, nil
}

func (a *App) VerifySecrets(ctx context.Context) error {
	rows, err := a.DB.Query(ctx, "SELECT secret FROM instances UNION ALL SELECT secret FROM config_versions UNION ALL SELECT secret FROM discoveries WHERE secret IS NOT NULL")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var blob []byte
		if err = rows.Scan(&blob); err != nil {
			return err
		}
		if _, err = a.unseal(blob); err != nil {
			return errors.New("MASTER_KEY cannot decrypt stored credentials")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if err := a.verifyMCPOAuthSecrets(ctx, a.DB); err != nil {
		return err
	}
	return a.verifyClientIntegrationSecrets(ctx)
}
func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (a *App) seal(s secrets) []byte {
	b, _ := json.Marshal(s)
	n := make([]byte, a.Key.NonceSize())
	rand.Read(n)
	return a.Key.Seal(n, n, b, nil)
}
func (a *App) unseal(b []byte) (secrets, error) {
	var s secrets
	if len(b) < a.Key.NonceSize() {
		return s, errors.New("invalid encrypted configuration")
	}
	v, e := a.Key.Open(nil, b[:a.Key.NonceSize()], b[a.Key.NonceSize():], nil)
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(v, &s)
	return s, e
}
func (a *App) lock(id string) *sync.Mutex {
	v, _ := a.Locks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, s string) {
	respond(w, status, map[string]string{"error": s})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	if e := d.Decode(v); e != nil {
		fail(w, 400, "无效 JSON 请求")
		return false
	}
	return true
}
func (a *App) loadInstance(ctx context.Context, id string) (Instance, error) {
	var i Instance
	var b, sec []byte
	e := a.DB.QueryRow(ctx, "SELECT config,secret,version,enabled,status,last_error FROM instances WHERE id=$1", id).Scan(&b, &sec, &i.Version, &i.Enabled, &i.Status, &i.LastError)
	if e != nil {
		return i, e
	}
	version, enabled, status, last := i.Version, i.Enabled, i.Status, i.LastError
	e = json.Unmarshal(b, &i)
	i.Version, i.Enabled, i.Status, i.LastError = version, enabled, status, last
	if e != nil {
		return i, e
	}
	s, e := a.unseal(sec)
	i.Env = s.Env
	i.Headers = s.Headers
	i.HeaderHelper = s.HeaderHelper
	if s.Args != nil {
		i.Args = s.Args
	}
	if s.URL != nil {
		i.URL = *s.URL
	}
	i.diagnosticSecrets = func() []string { return a.oauthDiagnosticValues(i.ID) }
	return i, e
}
func publicInstance(i Instance) Instance {
	i.HeaderHelperConfigured = i.HeaderHelper != ""
	i.HeaderHelper = ""
	if i.Args != nil {
		masked := make([]string, len(i.Args))
		for n := range masked {
			masked[n] = "<REDACTED>"
		}
		i.Args = masked
	}
	if i.URL != "" {
		u, e := url.Parse(i.URL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			i.URL = "<REDACTED_URL>"
		} else {
			u.User = nil
			if u.Path != "" && u.Path != "/" {
				u.Path = "/<REDACTED>"
				u.RawPath = ""
			}
			if u.RawQuery != "" {
				u.RawQuery = "redacted"
			}
			u.Fragment = ""
			u.RawFragment = ""
			i.URL = u.String()
		}
	}
	i.EnvKeys = []string{}
	i.HeaderKeys = []string{}
	for k := range i.Env {
		i.EnvKeys = append(i.EnvKeys, k)
	}
	for k := range i.Headers {
		i.HeaderKeys = append(i.HeaderKeys, k)
	}
	i.Env = nil
	i.Headers = nil
	return i
}
func (a *App) presentInstance(i Instance) Instance {
	if i.Transport == "http" {
		_, i.OAuthRequired = a.oauthChallenges.Load(i.ID)
	}
	i = publicInstance(i)
	i.Health = a.observeHealth(i)
	info := &RuntimeInfo{Health: i.Status}
	if value, ok := a.Runtime.Load(i.ID); ok {
		s := value.(*snapshot)
		if i.Health.Protocol.State != "passed" {
			info.Health = "disconnected"
		} else {
			info.Health = "connected"
		}
		info.StartedAt = &s.startedAt
		info.RestartCount = s.restartCount
		if s.pid > 0 && i.Health.Process.State == "running" {
			pid := s.pid
			info.PID = &pid
		}
	}
	i.Runtime = info
	return i
}
func configBytes(i Instance) []byte { b, _ := json.Marshal(publicInstance(i)); return b }
func instanceSecrets(i Instance) secrets {
	args := append([]string{}, i.Args...)
	return secrets{Env: i.Env, Headers: i.Headers, HeaderHelper: i.HeaderHelper, Args: args, URL: &i.URL}
}
