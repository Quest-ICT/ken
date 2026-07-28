// Package i18n gives Ken's human web UI runtime-reloadable, drop-in translations.
//
// English (locales/messages.properties) ships embedded as the default AND the
// fallback; Spanish (locales/messages_es.properties) ships too. To add or
// override a language, drop a `messages_<lang>.properties` file into the external
// i18n dir (KEN_I18N_DIR, default <data-dir>/i18n) — it is picked up at runtime,
// no recompile and no restart (changes are noticed within a couple of seconds).
//
// File format: UTF-8, `key = value`, `#`/`!` comments, blank lines ignored, `{0}`
// `{1}` … positional placeholders, and the usual `\n \t \\ \uXXXX` escapes. Each
// file declares its own endonym via the reserved key `lang.self_name` (shown in
// the language selector). A missing key falls back to English, then to the key
// itself (so gaps are visible, never blank). Scope: the human web UI only — the
// AI/MCP surface and the server logs stay English.
package i18n

import (
	"embed"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed locales/*.properties
var embeddedFS embed.FS

const (
	// DefaultLang is the built-in default and the fallback for any missing key.
	DefaultLang = "en"
	selfNameKey = "lang.self_name"
	reloadEvery = 2 * time.Second // throttle re-stat of the external dir
)

// Lang is a selectable language: its code (e.g. "es") and endonym (e.g. "Español").
type Lang struct {
	Code string
	Name string
}

// Manager holds the merged catalogs (embedded defaults overlaid with the external
// override dir) and reloads the external dir when its files change.
type Manager struct {
	dir string // external override dir; "" disables external overrides

	mu        sync.RWMutex
	cat       map[string]map[string]string // lang -> key -> value
	langs     []Lang                       // available languages, en first then alpha
	lastCheck time.Time
	sig       string // signature of external files (names|mtime|size) to detect change
}

// New builds a Manager, loading the embedded defaults and overlaying dir if it
// exists. dir need not exist yet — it is watched and picked up when created.
func New(dir string) *Manager {
	m := &Manager{dir: dir}
	m.reload()
	return m
}

// Languages returns the available languages (English first, then alphabetical) —
// the list the UI language selector renders.
func (m *Manager) Languages() []Lang {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Lang, len(m.langs))
	copy(out, m.langs)
	return out
}

// Has reports whether a language is available.
func (m *Manager) Has(lang string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.cat[lang]
	return ok
}

// T translates key in lang, substituting {0}, {1}, … with args. Falls back to
// English, then to the key itself. Safe for concurrent use.
func (m *Manager) T(lang, key string, args ...any) string {
	m.mu.RLock()
	v, ok := m.lookup(lang, key)
	m.mu.RUnlock()
	if !ok {
		return key
	}
	return substitute(v, args)
}

// TN is T with a simple plural: it resolves key.one when n == 1, else key.other,
// and makes {0} default to n (extra args fill {1}, {2}, …). en/es/fr share the
// one/other rule; languages needing richer plural forms use their own paired keys.
func (m *Manager) TN(lang, key string, n int, args ...any) string {
	suffix := ".other"
	if n == 1 {
		suffix = ".one"
	}
	m.mu.RLock()
	v, ok := m.lookup(lang, key+suffix)
	m.mu.RUnlock()
	if !ok {
		return key + suffix
	}
	return substitute(v, append([]any{n}, args...))
}

// lookup resolves key in lang → English → (not found). Caller holds RLock.
func (m *Manager) lookup(lang, key string) (string, bool) {
	if c := m.cat[lang]; c != nil {
		if v, ok := c[key]; ok {
			return v, true
		}
	}
	if lang != DefaultLang {
		if c := m.cat[DefaultLang]; c != nil {
			if v, ok := c[key]; ok {
				return v, true
			}
		}
	}
	return "", false
}

// MaybeReload re-reads the external dir if its files changed since the last check
// (throttled). Call once per request, not per T.
func (m *Manager) MaybeReload() {
	m.mu.RLock()
	fresh := time.Since(m.lastCheck) < reloadEvery
	old := m.sig
	m.mu.RUnlock()
	if fresh {
		return
	}
	sig := m.externalSig()
	if sig == old {
		m.mu.Lock()
		m.lastCheck = time.Now()
		m.mu.Unlock()
		return
	}
	m.reload()
}

// reload rebuilds the catalog from the embedded defaults plus the external dir.
func (m *Manager) reload() {
	cat := map[string]map[string]string{}

	// Embedded defaults first.
	if entries, err := embeddedFS.ReadDir("locales"); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".properties") {
				continue
			}
			if b, err := embeddedFS.ReadFile("locales/" + e.Name()); err == nil {
				overlay(cat, langOf(e.Name()), parse(b))
			}
		}
	}

	// External overrides (win over embedded; may add new languages).
	if m.dir != "" {
		if entries, err := os.ReadDir(m.dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !isMessages(e.Name()) {
					continue
				}
				if b, err := os.ReadFile(filepath.Join(m.dir, e.Name())); err == nil {
					overlay(cat, langOf(e.Name()), parse(b))
				}
			}
		}
	}

	// Ensure English always exists (so fallback never nil).
	if cat[DefaultLang] == nil {
		cat[DefaultLang] = map[string]string{}
	}

	langs := make([]Lang, 0, len(cat))
	for code, kv := range cat {
		name := kv[selfNameKey]
		if name == "" {
			name = code
		}
		langs = append(langs, Lang{Code: code, Name: name})
	}
	sort.Slice(langs, func(i, j int) bool {
		if langs[i].Code == DefaultLang {
			return true
		}
		if langs[j].Code == DefaultLang {
			return false
		}
		return langs[i].Code < langs[j].Code
	})

	m.mu.Lock()
	m.cat = cat
	m.langs = langs
	m.lastCheck = time.Now()
	m.sig = m.externalSigLocked()
	m.mu.Unlock()
}

// externalSig / externalSigLocked build a change signature from the external
// dir's *.properties files (name|modtime|size). "" when the dir is absent/empty.
func (m *Manager) externalSig() string {
	if m.dir == "" {
		return ""
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !isMessages(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		b.WriteString(e.Name())
		b.WriteByte('|')
		b.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
		b.WriteByte('|')
		b.WriteString(strconv.FormatInt(info.Size(), 10))
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *Manager) externalSigLocked() string { return m.externalSig() }

// --- helpers ---

func isMessages(name string) bool {
	return strings.HasPrefix(name, "messages") && strings.HasSuffix(name, ".properties")
}

// langOf maps a filename to a language code: messages.properties -> "en",
// messages_es.properties -> "es", messages_pt_BR.properties -> "pt_BR".
func langOf(filename string) string {
	base := strings.TrimSuffix(filename, ".properties")
	if base == "messages" {
		return DefaultLang
	}
	if code := strings.TrimPrefix(base, "messages_"); code != base && code != "" {
		return code
	}
	return DefaultLang
}

func overlay(cat map[string]map[string]string, lang string, kv map[string]string) {
	if cat[lang] == nil {
		cat[lang] = map[string]string{}
	}
	for k, v := range kv {
		cat[lang][k] = v
	}
}

// substitute replaces {0}, {1}, … with the string form of each arg.
func substitute(s string, args []any) string {
	if len(args) == 0 || !strings.Contains(s, "{") {
		return s
	}
	for i, a := range args {
		s = strings.ReplaceAll(s, "{"+strconv.Itoa(i)+"}", toStr(a))
	}
	return s
}

func toStr(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	default:
		return strings.TrimSpace(strings.Trim(strconv.Quote(toStrFallback(v)), `"`))
	}
}

func toStrFallback(a any) string {
	if s, ok := a.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}

// parse reads a UTF-8 .properties body into a key→value map.
func parse(b []byte) map[string]string {
	out := map[string]string{}
	s := strings.TrimPrefix(string(b), "\ufeff") // strip UTF-8 BOM
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == '!' {
			continue
		}
		sep := indexSep(line)
		if sep < 0 {
			continue
		}
		key := strings.TrimSpace(line[:sep])
		val := strings.TrimSpace(line[sep+1:])
		if key == "" {
			continue
		}
		out[key] = unescape(val)
	}
	return out
}

// indexSep finds the first unescaped '=' or ':' separator.
func indexSep(line string) int {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++ // skip escaped char
		case '=', ':':
			return i
		}
	}
	return -1
}

// unescape resolves \n \t \r \\ \: \= \uXXXX and \<space>.
func unescape(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 >= len(v) {
			b.WriteByte(v[i])
			continue
		}
		i++
		switch v[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'u':
			if i+4 < len(v) {
				if r, err := strconv.ParseUint(v[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		default:
			b.WriteByte(v[i]) // \\ \: \= \<space> etc → literal
		}
	}
	return b.String()
}
