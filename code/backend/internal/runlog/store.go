// Package runlog keeps bounded records in private daily runtime/exception files.
package runlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const Retention = 7 * 24 * time.Hour
const maxFileBytes = 8 << 20

var filePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-(runtime|exception)-\d{6}\.jsonl$`)
var uriPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)
var bearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[a-z0-9._~+/-]+=*`)
var secretPattern = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|authorization|master_key|setup_key)\s*[:=]\s*)(?:"[^"\n]*"|'[^'\n]*'|[^\s,;]+)`)

type Entry struct {
	ID      string         `json:"id"`
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}
type Store struct {
	root string
	mu   sync.Mutex
	now  func() time.Time
}
type Filter struct {
	Category, Level, Search, Cursor string
	From, To                        time.Time
	Limit                           int
}
type Page struct {
	Items []Entry `json:"items"`
	Next  string  `json:"next_cursor"`
	Days  int     `json:"retention_days"`
}
type cursor struct {
	File     string `json:"f"`
	Position int64  `json:"p"`
}

func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("系统日志目录不可用")
	}
	if err = os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	s := &Store{root: root, now: time.Now}
	if err = s.Prune(); err != nil {
		return nil, err
	}
	return s, nil
}
func Sanitize(text string) string {
	text = bearerPattern.ReplaceAllString(text, "Bearer <REDACTED>")
	text = secretPattern.ReplaceAllString(text, "${1}<REDACTED>")
	text = uriPattern.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "<REDACTED_URL>"
		}
		if u.User != nil {
			u.User = url.User("REDACTED")
		}
		u.RawQuery = ""
		u.Fragment = ""
		if u.Scheme == "http" || u.Scheme == "https" {
			u.Path = "/<REDACTED>"
			u.RawPath = ""
		}
		return u.String()
	})
	if r := []rune(text); len(r) > 8192 {
		return string(r[:8192]) + "…"
	}
	return text
}
func sensitive(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"password", "passwd", "authorization", "token", "secret", "api_key", "apikey", "credential"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}
func cleanAttr(key string, v any) any {
	if sensitive(key) {
		return "<REDACTED>"
	}
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case bool, int, int64, float64:
		return x
	case string:
		return Sanitize(x)
	default:
		return Sanitize(fmt.Sprint(x))
	}
}
func (s *Store) Write(e Entry) error {
	if e.Time.IsZero() {
		e.Time = s.now()
	}
	if e.Time.Before(s.now().Add(-Retention)) {
		return nil
	}
	e.Time = e.Time.UTC()
	e.ID = ""
	e.Message = Sanitize(e.Message)
	attrs := map[string]any{}
	n := 0
	for k, v := range e.Attrs {
		if n >= 32 {
			break
		}
		value := cleanAttr(k, v)
		if text, ok := value.(string); ok {
			r := []rune(text)
			if len(r) > 2048 {
				value = string(r[:2048]) + "…"
			}
		}
		attrs[k] = value
		n++
	}
	e.Attrs = attrs
	category := "runtime"
	if e.Level == "WARN" || e.Level == "ERROR" {
		category = "exception"
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := e.Time.UTC().Format("2006-01-02") + "-" + category
	for part := 0; part < 1000000; part++ {
		name := filepath.Join(s.root, fmt.Sprintf("%s-%06d.jsonl", prefix, part))
		info, err := os.Lstat(name)
		if err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("日志文件不是普通文件")
			}
			if info.Size()+int64(len(raw)) > maxFileBytes {
				continue
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(raw)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return errors.New("日志分片数量已达上限")
}
func (s *Store) Prune() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	cutoffTime := s.now().Add(-Retention)
	cutoff := cutoffTime.UTC().Format("2006-01-02")
	for _, f := range files {
		if !filePattern.MatchString(f.Name()) || f.Name()[:10] > cutoff {
			continue
		}
		info, err := f.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if f.Name()[:10] == cutoff {
			if err = s.prunePartial(filepath.Join(s.root, f.Name()), cutoffTime); err != nil {
				return err
			}
			continue
		}
		if err = os.Remove(filepath.Join(s.root, f.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Called under mu: atomically rewrite the boundary day, never truncate an active writer.
func (s *Store) prunePartial(name string, cutoff time.Time) error {
	source, err := os.Open(name)
	if err != nil {
		return err
	}
	defer source.Close()
	tmp, err := os.CreateTemp(s.root, ".retention-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	scan := bufio.NewScanner(source)
	scan.Buffer(make([]byte, 64<<10), 512<<10)
	removed := false
	for scan.Scan() {
		var e Entry
		if json.Unmarshal(scan.Bytes(), &e) != nil || e.Time.Before(cutoff) {
			removed = true
			continue
		}
		if _, err = tmp.Write(append(append([]byte{}, scan.Bytes()...), '\n')); err != nil {
			return err
		}
	}
	if err = scan.Err(); err != nil {
		return err
	}
	if !removed {
		return nil
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}
func (s *Store) RunPruner(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Prune(); err != nil {
				slog.Error("系统日志保留策略清理失败", "event", "runtime_log_cleanup_failed", "detail", err.Error())
			}
		}
	}
}
func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Store) Query(ctx context.Context, f Filter) (Page, error) {
	out := Page{Items: []Entry{}, Days: 7}
	if f.Category != "runtime" && f.Category != "exception" {
		return out, errors.New("无效日志分类")
	}
	if f.Level != "" && f.Level != "INFO" && f.Level != "WARN" && f.Level != "ERROR" {
		return out, errors.New("无效日志级别")
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if len(f.Search) > 200 {
		return out, errors.New("搜索内容过长")
	}
	cutoff := s.now().Add(-Retention)
	if f.From.IsZero() || f.From.Before(cutoff) {
		f.From = cutoff
	}
	if f.To.IsZero() {
		f.To = s.now()
	}
	if f.From.After(f.To) {
		return out, errors.New("开始时间晚于结束时间")
	}
	var after cursor
	if f.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil || json.Unmarshal(b, &after) != nil || !filePattern.MatchString(after.File) || !strings.Contains(after.File, "-"+f.Category+"-") || after.Position < 0 {
			return out, errors.New("分页位置无效，请刷新")
		}
	}
	files, err := os.ReadDir(s.root)
	if err != nil {
		return out, err
	}
	var names []string
	for _, file := range files {
		if filePattern.MatchString(file.Name()) && strings.Contains(file.Name(), "-"+f.Category+"-") && file.Name()[:10] >= f.From.UTC().Format("2006-01-02") {
			names = append(names, file.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	query := strings.ToLower(f.Search)
	last := cursor{}
	for _, name := range names {
		if after.File != "" && name > after.File {
			continue
		}
		info, err := os.Lstat(filepath.Join(s.root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return out, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(filepath.Join(s.root, name))
		if err != nil {
			return out, err
		}
		end := info.Size()
		if name == after.File {
			end = min(end, after.Position)
		}
		done := false
		err = reverseLines(ctx, file, end, func(raw []byte, pos int64) bool {
			var e Entry
			if json.Unmarshal(raw, &e) != nil || e.Time.Before(f.From) || e.Time.After(f.To) || (f.Level != "" && e.Level != f.Level) || (query != "" && !strings.Contains(strings.ToLower(string(raw)), query)) {
				return true
			}
			if len(out.Items) >= f.Limit {
				out.Next = encodeCursor(last)
				done = true
				return false
			}
			last = cursor{name, pos}
			e.ID = encodeCursor(last)
			out.Items = append(out.Items, e)
			return true
		})
		file.Close()
		if err != nil {
			return out, err
		}
		if done {
			break
		}
	}
	return out, nil
}
func reverseLines(ctx context.Context, f *os.File, end int64, visit func([]byte, int64) bool) error {
	const blockSize = 64 << 10
	var suffix []byte
	for end > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		start := max(int64(0), end-blockSize)
		block := make([]byte, end-start)
		n, err := f.ReadAt(block, start)
		if err != nil && err != io.EOF {
			return err
		}
		block = block[:n]
		tail := len(block)
		for i := len(block) - 1; i >= 0; i-- {
			if block[i] == '\n' {
				line := append(append([]byte{}, block[i+1:tail]...), suffix...)
				if len(line) > 0 && !visit(line, start+int64(i)+1) {
					return nil
				}
				suffix = nil
				tail = i
			}
		}
		suffix = append(append([]byte{}, block[:tail]...), suffix...)
		if len(suffix) > 512<<10 {
			return errors.New("日志记录过大，无法读取")
		}
		end = start
	}
	if len(bytes.TrimSpace(suffix)) > 0 {
		visit(suffix, 0)
	}
	return nil
}

type handler struct {
	store *Store
	next  slog.Handler
	attrs []slog.Attr
	group string
}

func (s *Store) Handler(next slog.Handler) slog.Handler               { return &handler{store: s, next: next} }
func (h *handler) Enabled(ctx context.Context, level slog.Level) bool { return level >= slog.LevelInfo }
func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := map[string]any{}
	var add func(string, slog.Attr)
	add = func(group string, a slog.Attr) {
		a.Value = a.Value.Resolve()
		key := group + a.Key
		if a.Value.Kind() == slog.KindGroup {
			for _, v := range a.Value.Group() {
				add(key+".", v)
			}
			return
		}
		attrs[key] = cleanAttr(key, a.Value.Any())
	}
	for _, a := range h.attrs {
		add("", a)
	}
	r.Attrs(func(a slog.Attr) bool { add(h.group, a); return true })
	e := Entry{Time: r.Time, Level: r.Level.String(), Message: Sanitize(r.Message), Attrs: attrs}
	if r.Level >= slog.LevelError {
		e.Level = "ERROR"
	} else if r.Level >= slog.LevelWarn {
		e.Level = "WARN"
	} else {
		e.Level = "INFO"
	}
	err := h.store.Write(e)
	if err != nil {
		fmt.Fprintln(os.Stderr, "系统日志写入失败：", Sanitize(err.Error()))
	}
	if h.next != nil {
		safe := slog.NewRecord(r.Time, r.Level, e.Message, r.PC)
		for k, v := range attrs {
			safe.AddAttrs(slog.Any(k, v))
		}
		_ = h.next.Handle(ctx, safe)
	}
	return err
}
func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copy := *h
	copy.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &copy
}
func (h *handler) WithGroup(name string) slog.Handler {
	copy := *h
	copy.group += name + "."
	return &copy
}
