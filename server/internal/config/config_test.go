package config

import "testing"

const key32 = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=" // 32 × 'B'

func env(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := kv[k]
		return v, ok
	}
}

func base() map[string]string {
	return map[string]string{
		"IMMICH_URL": "http://immich:2283/api/", "PUBLIC_URL": "https://photos.example.com/", "COOKIE_KEY": key32,
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Parse(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ImmichURL != "http://immich:2283" || cfg.PublicURL != "https://photos.example.com" {
		t.Errorf("urls: %q %q", cfg.ImmichURL, cfg.PublicURL)
	}
	if !cfg.ImmichKeyLinks || !cfg.ImmichSlugLinks || cfg.TokenLinks() || !cfg.ShowDates || cfg.ShowDescriptions {
		t.Errorf("link/visibility defaults: %+v", cfg)
	}
}

func TestRequiredAndInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"missing":       {},
		"bad cookie":    {"COOKIE_KEY": "short"},
		"relative url":  {"PUBLIC_URL": "photos.example.com"},
		"query in url":  {"IMMICH_URL": "http://immich:2283?x=1"},
		"bad bool":      {"SHOW_DATES": "maybe"},
		"bad size":      {"CACHE_MAX_BYTES": "lots"},
		"bad duration":  {"SHARE_TTL": "-1s"},
		"zero queue":    {"SANITIZE_QUEUE": "0"},
		"bad proxy":     {"TRUSTED_PROXIES": "10.0.0.0/8,nope"},
		"no link kinds": {"IMMICH_KEY_LINKS": "false", "IMMICH_SLUG_LINKS": "false"},
		"bad log level": {"LOG_LEVEL": "loud"},
		"http public":   {"PUBLIC_URL": "http://photos.example.com"},
	}
	for name, over := range cases {
		kv := base()
		if name == "missing" {
			kv = map[string]string{}
		}
		for k, v := range over {
			kv[k] = v
		}
		if _, err := Parse(env(kv)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLocalPublicURL(t *testing.T) {
	for _, u := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		kv := base()
		kv["PUBLIC_URL"] = u
		if _, err := Parse(env(kv)); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
}

func TestLinkKeys(t *testing.T) {
	kv := base()
	kv["IMMICH_KEY_LINKS"], kv["IMMICH_SLUG_LINKS"] = "false", "false"
	kv["LINK_KEYS"] = "1:" + key32 + ":retired, 2:" + key32 + ":active"
	cfg, err := Parse(env(kv))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TokenLinks() || len(cfg.LinkKeys) != 2 || cfg.LinkKeys[0].Active || !cfg.LinkKeys[1].Active {
		t.Fatalf("keys: %+v", cfg.LinkKeys)
	}
	for _, bad := range []string{
		"1:" + key32 + ":active,1:" + key32 + ":retired", // duplicate id
		"1:c2hvcnQ=:active",        // short key
		"300:" + key32 + ":active", // id out of range
		"1:" + key32,               // no state
		"1:" + key32 + ":paused",   // unknown state
	} {
		if _, err := ParseLinkKeys(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"10GiB": 10 << 30, "512MiB": 512 << 20, "4GB": 4e9, "1000": 1000, "7B": 7} {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "GiB", "-1GiB", "1.5GiB", "ten"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
