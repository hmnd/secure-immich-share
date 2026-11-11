// Package config parses the environment into a plain Config struct.
// Tests construct Config directly; Load is only used by cmd and the harness.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// LinkKey is one LINK_KEYS entry.
type LinkKey struct {
	ID     byte
	Secret []byte // 32 bytes
	Active bool
}

// Config is the parsed runtime configuration.
type Config struct {
	ImmichURL string // without trailing slash and without /api
	PublicURL string // without trailing slash

	ImmichKeyLinks  bool
	ImmichSlugLinks bool
	// LinkKeys enables /t/{token} links when non-empty.
	LinkKeys  []LinkKey
	CookieKey []byte

	ListenAddr     string
	TrustedProxies []netip.Prefix

	CacheDir       string
	CacheMaxBytes  int64
	MaxSourceBytes int64

	SanitizeConcurrency int
	SanitizeQueue       int
	SanitizeTimeout     time.Duration

	ShareTTL time.Duration

	ShowDescriptions bool
	ShowDates        bool
	OGPreviews       bool

	PrewarmVideos      bool
	PrewarmImages      bool
	PrewarmConcurrency int
	PrewarmTTL         time.Duration

	FailedTTL time.Duration

	ZipJobs        int
	ZipConcurrency int
	ZipTTL         time.Duration

	ShutdownGrace time.Duration

	LogLevel string

	// ViteDev loads the islands from the Vite dev server in front of this
	// one (development only).
	ViteDev bool
}

// TokenLinks reports whether /t/{token} links are enabled.
func (c Config) TokenLinks() bool { return len(c.LinkKeys) > 0 }

// Load parses the process environment.
func Load() (Config, error) {
	return Parse(os.LookupEnv)
}

// Parse builds a Config from a lookup function (os.LookupEnv in production).
func Parse(lookup func(string) (string, bool)) (Config, error) {
	p := parser{lookup: lookup}
	var cfg Config

	cfg.ImmichURL = strings.TrimSuffix(strings.TrimRight(p.required("IMMICH_URL"), "/"), "/api")
	cfg.PublicURL = strings.TrimRight(p.required("PUBLIC_URL"), "/")
	cookieKey := p.required("COOKIE_KEY")
	if len(p.missing) > 0 {
		return Config{}, fmt.Errorf("missing required env vars: %s", strings.Join(p.missing, ", "))
	}
	if err := absoluteURL(cfg.ImmichURL); err != nil {
		return Config{}, fmt.Errorf("invalid IMMICH_URL: %w", err)
	}
	if err := absoluteURL(cfg.PublicURL); err != nil {
		return Config{}, fmt.Errorf("invalid PUBLIC_URL: %w", err)
	}
	if err := secureOrLocal(cfg.PublicURL); err != nil {
		return Config{}, fmt.Errorf("invalid PUBLIC_URL: %w", err)
	}
	var err error
	if cfg.CookieKey, err = parseKey32(cookieKey); err != nil {
		return Config{}, errors.New("invalid COOKIE_KEY: want base64 of 32 bytes (openssl rand -base64 32)")
	}

	cfg.ImmichKeyLinks = p.bool("IMMICH_KEY_LINKS", true)
	cfg.ImmichSlugLinks = p.bool("IMMICH_SLUG_LINKS", true)
	if v := p.optional("LINK_KEYS", ""); v != "" {
		if cfg.LinkKeys, err = ParseLinkKeys(v); err != nil {
			return Config{}, fmt.Errorf("invalid LINK_KEYS: %w", err)
		}
	}

	cfg.ListenAddr = p.optional("LISTEN_ADDR", ":8080")
	if v := p.optional("TRUSTED_PROXIES", ""); v != "" {
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			prefix, perr := netip.ParsePrefix(part)
			if perr != nil {
				addr, aerr := netip.ParseAddr(part)
				if aerr != nil {
					return Config{}, fmt.Errorf("invalid TRUSTED_PROXIES entry %q", part)
				}
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, prefix.Masked())
		}
	}

	cfg.CacheDir = p.optional("CACHE_DIR", "/cache")
	cfg.CacheMaxBytes = p.bytes("CACHE_MAX_BYTES", 10<<30)
	cfg.MaxSourceBytes = p.bytes("MAX_SOURCE_BYTES", 4<<30)

	cfg.SanitizeConcurrency = p.int("SANITIZE_CONCURRENCY", runtime.NumCPU(), 1)
	cfg.SanitizeQueue = p.int("SANITIZE_QUEUE", 64, 1)
	cfg.SanitizeTimeout = p.duration("SANITIZE_TIMEOUT", 120*time.Second)

	cfg.ShareTTL = p.duration("SHARE_TTL", 60*time.Second)

	cfg.ShowDescriptions = p.bool("SHOW_DESCRIPTIONS", false)
	cfg.ShowDates = p.bool("SHOW_DATES", true)
	cfg.OGPreviews = p.bool("OG_PREVIEWS", true)

	cfg.PrewarmVideos = p.bool("PREWARM_VIDEOS", true)
	cfg.PrewarmImages = p.bool("PREWARM_IMAGES", false)
	cfg.PrewarmConcurrency = p.int("PREWARM_CONCURRENCY", 1, 1)
	cfg.PrewarmTTL = p.duration("PREWARM_TTL", 10*time.Minute)

	cfg.FailedTTL = p.duration("FAILED_TTL", 10*time.Minute)

	cfg.ZipJobs = p.int("ZIP_JOBS", 2, 1)
	cfg.ZipConcurrency = p.int("ZIP_CONCURRENCY", max(2, cfg.SanitizeConcurrency/2), 1)
	cfg.ZipTTL = p.duration("ZIP_TTL", 15*time.Minute)

	cfg.ShutdownGrace = p.duration("SHUTDOWN_GRACE", 30*time.Second)

	cfg.ViteDev = p.bool("VITE_DEV", false)

	cfg.LogLevel = p.optional("LOG_LEVEL", "info")
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		p.errs = append(p.errs, fmt.Errorf("invalid LOG_LEVEL %q (want debug, info, warn or error)", cfg.LogLevel))
	}

	if len(p.errs) > 0 {
		return Config{}, errors.Join(p.errs...)
	}
	if !cfg.ImmichKeyLinks && !cfg.ImmichSlugLinks && !cfg.TokenLinks() {
		return Config{}, errors.New("no link kind enabled: set IMMICH_KEY_LINKS, IMMICH_SLUG_LINKS or LINK_KEYS")
	}
	return cfg, nil
}

// ParseLinkKeys parses `id:base64key:active|retired[,…]`. linkref checks
// that exactly one key is active.
func ParseLinkKeys(v string) ([]LinkKey, error) {
	var keys []LinkKey
	seen := map[byte]bool{}
	for part := range strings.SplitSeq(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ":")
		if len(fields) != 3 {
			return nil, fmt.Errorf("entry %d: want id:base64key:active|retired", len(keys)+1)
		}
		id, err := strconv.ParseUint(fields[0], 10, 8)
		if err != nil {
			return nil, fmt.Errorf("entry %d: id must be 0-255", len(keys)+1)
		}
		if seen[byte(id)] {
			return nil, fmt.Errorf("duplicate key id %d", id)
		}
		seen[byte(id)] = true
		secret, err := parseKey32(fields[1])
		if err != nil {
			return nil, fmt.Errorf("key %d: want base64 of 32 bytes", id)
		}
		k := LinkKey{ID: byte(id), Secret: secret}
		switch fields[2] {
		case "active":
			k.Active = true
		case "retired":
		default:
			return nil, fmt.Errorf("key %d: state must be active or retired", id)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

type parser struct {
	lookup  func(string) (string, bool)
	missing []string
	errs    []error
}

func (p *parser) optional(key, def string) string {
	if v, ok := p.lookup(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (p *parser) required(key string) string {
	v := p.optional(key, "")
	if v == "" {
		p.missing = append(p.missing, key)
	}
	return v
}

func (p *parser) bool(key string, def bool) bool {
	v := p.optional(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("invalid %s: want true or false", key))
	}
	return b
}

func (p *parser) int(key string, def, minimum int) int {
	v := p.optional(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum {
		p.errs = append(p.errs, fmt.Errorf("invalid %s: want an integer ≥ %d", key, minimum))
	}
	return n
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v := p.optional(key, "")
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		p.errs = append(p.errs, fmt.Errorf("invalid %s: want a positive duration such as 30s or 10m", key))
	}
	return d
}

func (p *parser) bytes(key string, def int64) int64 {
	v := p.optional(key, "")
	if v == "" {
		return def
	}
	n, err := ParseBytes(v)
	if err != nil {
		p.errs = append(p.errs, fmt.Errorf("invalid %s: %w", key, err))
	}
	return n
}

// ParseBytes parses sizes such as 512MiB, 10GiB, 4GB or a plain byte count.
func ParseBytes(v string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	num, mult := v, int64(1)
	for _, u := range units {
		if s, ok := strings.CutSuffix(v, u.suffix); ok {
			num, mult = strings.TrimSpace(s), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("want a positive size such as 512MiB or 10GiB")
	}
	return n * mult, nil
}

func absoluteURL(v string) error {
	u, err := url.Parse(v)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q is not an absolute http(s) URL", v)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q must not have a query or fragment", v)
	}
	return nil
}

// secureOrLocal rejects plain-http URLs except on localhost: unlock cookies
// are Secure, so browsers drop them over http elsewhere.
func secureOrLocal(v string) error {
	u, err := url.Parse(v)
	if err != nil {
		return err
	}
	if u.Scheme == "https" || u.Hostname() == "localhost" {
		return nil
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%q must use https (plain http only works on localhost)", v)
}

// parseKey32 decodes 32 bytes from base64 (std or URL alphabet, padded or not).
func parseKey32(v string) ([]byte, error) {
	encs := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encs {
		if key, err := enc.DecodeString(v); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("not base64 of 32 bytes")
}
