package agyauth

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type memKeychain struct {
	mu          sync.Mutex
	items       map[string][]byte
	lastTrusted map[string][]string
}

func newMemKeychain() *memKeychain {
	return &memKeychain{
		items:       make(map[string][]byte),
		lastTrusted: make(map[string][]string),
	}
}

func kcKey(svc, acct string) string { return svc + "\x00" + acct }

func (m *memKeychain) Get(service, account string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[kcKey(service, account)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memKeychain) Set(service, account string, secret []byte, trustedApps []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(secret))
	copy(cp, secret)
	m.items[kcKey(service, account)] = cp
	m.lastTrusted[kcKey(service, account)] = append([]string(nil), trustedApps...)
	return nil
}

func (m *memKeychain) Delete(service, account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := kcKey(service, account)
	if _, ok := m.items[k]; !ok {
		return ErrNotFound
	}
	delete(m.items, k)
	return nil
}

func makeFakeJWT(email string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"123","email":"` + email + `"}`))
	return hdr + "." + body + ".sig"
}

func TestSaveListSwitchRemoveProfiles(t *testing.T) {
	tmp := t.TempDir()
	profilesPath := filepath.Join(tmp, "agy-profiles.json")
	quotaPath := filepath.Join(tmp, "agy-quota.json")

	// Seed official quota file for live quota enrichment.
	reset5h := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	resetWk := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	quotaJSON := `{
		"version": 2,
		"accounts": {
			"alice@example.com": {
				"email": "alice@example.com",
				"plan_tier": "Google AI Pro",
				"quota": {
					"gemini-5h": {"remaining_fraction": 0.903, "reset_time": "` + reset5h + `"},
					"gemini-weekly": {"remaining_fraction": 0.409, "reset_time": "` + resetWk + `"}
				}
			},
			"bob@example.com": {
				"email": "bob@example.com",
				"plan_tier": "Google AI Ultra",
				"quota": {
					"gemini-5h": {"remaining_fraction": 0.565, "reset_time": "` + reset5h + `"},
					"gemini-weekly": {"remaining_fraction": 0.878, "reset_time": "` + resetWk + `"}
				}
			}
		}
	}`
	if err := os.WriteFile(quotaPath, []byte(quotaJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	kc := newMemKeychain()
	aliceSecret := []byte("{\"access_token\":\"tok-alice-secret\",\"refresh_token\":\"ref-alice\",\"email\":\"alice@example.com\"}\n")
	bobJSON := `{"access_token":"tok-bob-secret","id_token":"` + makeFakeJWT("bob@example.com") + `"}`
	bobSecret := []byte("go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(bobJSON)))

	// 1. User logs in as Alice -> save profile with auto-detected name & email.
	_ = kc.Set(DefaultAgyService, DefaultAgyAccount, aliceSecret, nil)

	mgr := New(Options{
		ProfilesPath: profilesPath,
		AgyQuotaPath: quotaPath,
		Store:        kc,
		TrustedApps:  []string{"/usr/bin/security", "/opt/homebrew/bin/agy"},
	})

	pAlice, err := mgr.SaveCurrent("", "")
	if err != nil {
		t.Fatalf("SaveCurrent alice: %v", err)
	}
	if pAlice.Name != "alice" || pAlice.Email != "alice@example.com" {
		t.Fatalf("unexpected alice profile: %+v", pAlice)
	}

	// Verify NO plaintext token leaked into agy-profiles.json!
	rawMeta, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read profiles.json: %v", err)
	}
	if strings.Contains(string(rawMeta), "tok-alice-secret") || strings.Contains(string(rawMeta), "ref-alice") {
		t.Fatalf("secret leaked into agy-profiles.json: %s", string(rawMeta))
	}

	// 2. User logs in as Bob (go-keyring-base64 + JWT id_token) -> save profile "work".
	_ = kc.Set(DefaultAgyService, DefaultAgyAccount, bobSecret, nil)
	pBob, err := mgr.SaveCurrent("work", "")
	if err != nil {
		t.Fatalf("SaveCurrent bob: %v", err)
	}
	if pBob.Name != "work" || pBob.Email != "bob@example.com" {
		t.Fatalf("unexpected bob profile: %+v", pBob)
	}

	// 3. List should show both profiles, Bob active, and live quotas enriched!
	list, active, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if active == nil || !active.Present || active.EmailHint != "bob@example.com" {
		t.Fatalf("unexpected active slot: %+v", active)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(list))
	}
	if list[0].Name != "alice" || list[0].Active || list[0].PlanTier != "Google AI Pro" || !strings.Contains(list[0].Quota5h, "90.3%") {
		t.Fatalf("unexpected alice status: %+v", list[0])
	}
	if list[1].Name != "work" || !list[1].Active || list[1].PlanTier != "Google AI Ultra" || !strings.Contains(list[1].Quota5h, "56.5%") {
		t.Fatalf("unexpected bob status: %+v", list[1])
	}

	// 4. Switch back to Alice using email prefix!
	switched, err := mgr.Switch("alice@example")
	if err != nil {
		t.Fatalf("Switch alice: %v", err)
	}
	if switched.Name != "alice" {
		t.Fatalf("expected alice, got %+v", switched)
	}
	gotActiveBytes, err := kc.Get(DefaultAgyService, DefaultAgyAccount)
	if err != nil {
		t.Fatalf("kc.Get active: %v", err)
	}
	if !bytes.Equal(gotActiveBytes, aliceSecret) {
		t.Fatalf("exact raw credential bytes mismatch after switch")
	}
	if apps := kc.lastTrusted[kcKey(DefaultAgyService, DefaultAgyAccount)]; len(apps) != 2 {
		t.Fatalf("expected trusted apps passed to Keychain Set, got %v", apps)
	}

	// 5. List should now show Alice active and Bob inactive.
	list2, _, err := mgr.List()
	if err != nil {
		t.Fatalf("List 2: %v", err)
	}
	if !list2[0].Active || list2[1].Active {
		t.Fatalf("expected alice active / bob inactive, got %+v", list2)
	}

	// 6. Remove Bob -> vault entry removed, active Alice untouched.
	removed, err := mgr.Remove("bob@example.com")
	if err != nil {
		t.Fatalf("Remove bob: %v", err)
	}
	if removed.Name != "work" {
		t.Fatalf("unexpected removed profile: %+v", removed)
	}
	if _, err := kc.Get(VaultService, "work"); err != ErrNotFound {
		t.Fatalf("expected work vault key deleted, got err=%v", err)
	}
	if cur, err := kc.Get(DefaultAgyService, DefaultAgyAccount); err != nil || !bytes.Equal(cur, aliceSecret) {
		t.Fatalf("active slot should remain Alice after removing Bob")
	}
}

func TestBinaryPayloadPreservationAndHelpers(t *testing.T) {
	tmp := t.TempDir()
	kc := newMemKeychain()
	binSecret := []byte{0x00, 0x0a, 0x7b, 0x22, 0xff, 0xfe, 0x01, 0x02}
	_ = kc.Set(DefaultAgyService, DefaultAgyAccount, binSecret, nil)

	mgr := New(Options{
		ProfilesPath: filepath.Join(tmp, "p.json"),
		AgyQuotaPath: filepath.Join(tmp, "q.json"),
		Store:        kc,
	})
	if _, err := mgr.SaveCurrent("binprof", "bin@example.com"); err != nil {
		t.Fatalf("SaveCurrent binary: %v", err)
	}
	_ = kc.Delete(DefaultAgyService, DefaultAgyAccount)
	if _, err := mgr.Switch("binprof"); err != nil {
		t.Fatalf("Switch binary: %v", err)
	}
	got, err := kc.Get(DefaultAgyService, DefaultAgyAccount)
	if err != nil || !bytes.Equal(got, binSecret) {
		t.Fatalf("binary round-trip failed: err=%v got=%x want=%x", err, got, binSecret)
	}

	if !looksLikeLowerHex("7b2261223a317d0a") {
		t.Fatalf("expected valid lower hex")
	}
	if looksLikeLowerHex("go-keyring-base64:abc") || looksLikeLowerHex("abc") || looksLikeLowerHex("7B22") {
		t.Fatalf("unexpected hex positive")
	}
	if q := quoteSecurityArg(`foo "bar" \baz`); q != `"foo \"bar\" \\baz"` {
		t.Fatalf("unexpected quoteSecurityArg: %q", q)
	}
}

func TestResolveProfileDisambiguation(t *testing.T) {
	profiles := []*Profile{
		{Name: "work-us", Email: "dev-us@example.com"},
		{Name: "work-eu", Email: "dev-eu@example.com"},
		{Name: "personal", Email: "me@example.com"},
	}
	if p, err := resolveProfile(profiles, "work-us"); err != nil || p.Name != "work-us" {
		t.Fatalf("exact alias failed: %v %+v", err, p)
	}
	if p, err := resolveProfile(profiles, "ME@EXAMPLE.COM"); err != nil || p.Name != "personal" {
		t.Fatalf("case-insensitive email failed: %v %+v", err, p)
	}
	if p, err := resolveProfile(profiles, "pers"); err != nil || p.Name != "personal" {
		t.Fatalf("unique prefix failed: %v %+v", err, p)
	}
	if _, err := resolveProfile(profiles, "work-"); err == nil || !strings.Contains(err.Error(), "多个账号") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	if _, err := resolveProfile(profiles, "missing"); err == nil {
		t.Fatalf("expected not-found error")
	}

	// Ensure profilesFile JSON marshals cleanly without sensitive fields.
	b, _ := json.Marshal(profilesFile{Version: 1, Profiles: profiles})
	if !strings.Contains(string(b), "work-us") {
		t.Fatalf("unexpected json: %s", string(b))
	}
}

func TestExpiryParsingCustomTTLAndAutoSync(t *testing.T) {
	// 1. ParseDurationExtended coverage.
	if ms, err := ParseDurationExtended("7d"); err != nil || ms != 7*24*3600*1000 {
		t.Fatalf("7d mismatch: %d %v", ms, err)
	}
	if ms, err := ParseDurationExtended("1d12h"); err != nil || ms != 36*3600*1000 {
		t.Fatalf("1d12h mismatch: %d %v", ms, err)
	}
	if ms, err := ParseDurationExtended("2w"); err != nil || ms != 14*24*3600*1000 {
		t.Fatalf("2w mismatch: %d %v", ms, err)
	}
	if ms, err := ParseDurationExtended("off"); err != nil || ms != -1 {
		t.Fatalf("off mismatch: %d %v", ms, err)
	}
	if ms, err := ParseDurationExtended("auto"); err != nil || ms != 0 {
		t.Fatalf("auto mismatch: %d %v", ms, err)
	}
	if _, err := ParseDurationExtended("bogus"); err == nil {
		t.Fatalf("expected error on invalid duration")
	}

	// 2. Clock-controlled expiry transitions + refresh_token_expires_in parsing + auto-sync before switch.
	tmp := t.TempDir()
	profilesPath := filepath.Join(tmp, "agy-profiles.json")
	kc := newMemKeychain()

	baseNow := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	curNow := baseNow
	mgr := New(Options{
		ProfilesPath: profilesPath,
		AgyQuotaPath: filepath.Join(tmp, "q.json"),
		Store:        kc,
		NowFunc:      func() time.Time { return curNow },
	})

	// Alice has explicit refresh_token_expires_in = 3 days (259200s).
	aliceV1 := []byte(`{"email":"alice@example.com","access_token":"a1","refresh_token":"r1","expiry":"2026-11-26T13:00:00Z","refresh_token_expires_in":259200}`)
	_ = kc.Set(DefaultAgyService, DefaultAgyAccount, aliceV1, nil)
	pAlice, err := mgr.SaveCurrent("alice", "")
	if err != nil {
		t.Fatalf("SaveCurrent alice: %v", err)
	}
	if !pAlice.HasRefreshToken || pAlice.RefreshExpiresAt != baseNow.Add(72*time.Hour).UnixMilli() {
		t.Fatalf("alice refresh expiry mismatch: %+v", pAlice)
	}

	// Bob has only a short access_token (no refresh_token), expires in 1h.
	bobSecret := []byte(`{"email":"bob@example.com","access_token":"b1","expiry":"2026-11-26T13:00:00Z"}`)
	_ = kc.Set(DefaultAgyService, DefaultAgyAccount, bobSecret, nil)
	pBob, err := mgr.SaveCurrent("bob", "")
	if err != nil {
		t.Fatalf("SaveCurrent bob: %v", err)
	}
	if pBob.HasRefreshToken || pBob.TokenExpiresAt != baseNow.Add(time.Hour).UnixMilli() {
		t.Fatalf("bob token expiry mismatch: %+v", pBob)
	}

	// Advance clock by 50 minutes -> Bob (access-only, 10m left) is ExpiringSoon, Alice is healthy.
	curNow = baseNow.Add(50 * time.Minute)
	_, stBob, err := mgr.InspectProfile("bob")
	if err != nil || !stBob.ExpiringSoon || stBob.Expired {
		t.Fatalf("expected bob ExpiringSoon at +50m, got %+v err=%v", stBob, err)
	}
	_, stAlice, err := mgr.InspectProfile("alice")
	if err != nil || stAlice.ExpiringSoon || stAlice.Expired {
		t.Fatalf("expected alice healthy at +50m, got %+v err=%v", stAlice, err)
	}

	// Advance clock to +60h (12h left for Alice's 72h refresh token) -> Alice ExpiringSoon, Bob Expired!
	curNow = baseNow.Add(60 * time.Hour)
	_, stBob2, _ := mgr.InspectProfile("bob")
	_, stAlice2, _ := mgr.InspectProfile("alice")
	if !stBob2.Expired || !stAlice2.ExpiringSoon || stAlice2.Expired {
		t.Fatalf("unexpected +60h states: bob=%+v alice=%+v", stBob2, stAlice2)
	}

	// Test SetTTL custom override on Alice -> 30d from SavedAt makes Alice healthy again!
	ttl30d, _ := ParseDurationExtended("30d")
	_, stAlice30d, err := mgr.SetTTL("alice", ttl30d)
	if err != nil || stAlice30d.Expired || stAlice30d.ExpiringSoon || stAlice30d.Source != "custom_ttl" {
		t.Fatalf("expected alice healthy after 30d custom TTL, got %+v err=%v", stAlice30d, err)
	}

	// Test auto-sync before switch: simulate agy refreshing Alice's token in live Keychain, then switch to Bob.
	aliceV2 := []byte(`{"email":"alice@example.com","access_token":"a2-refreshed","refresh_token":"r2","expiry":"2026-11-29T01:00:00Z"}`)
	_ = kc.Set(DefaultAgyService, DefaultAgyAccount, aliceV2, nil)
	if _, err := mgr.Switch("bob"); err != nil {
		t.Fatalf("Switch bob: %v", err)
	}
	// Vault backup for Alice MUST now contain aliceV2 (silently synced before overwriting active slot with Bob)!
	backedUpAlice, err := kc.Get(VaultService, "alice")
	if err != nil || !bytes.Equal(backedUpAlice, aliceV2) {
		t.Fatalf("expected alice vault auto-synced to v2 before switching to bob, got err=%v val=%s", err, string(backedUpAlice))
	}
}

func TestProxyPropagationAndScutilParsing(t *testing.T) {
	scutilSample := []byte(`<dictionary> {
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7891
  SOCKSProxy : 127.0.0.1
}`)
	hp, hsp, ap := parseScutilProxyOutput(scutilSample)
	if hp != "http://127.0.0.1:7890" || hsp != "http://127.0.0.1:7890" || ap != "socks5://127.0.0.1:7891" {
		t.Fatalf("unexpected scutil parse: http=%q https=%q all=%q", hp, hsp, ap)
	}

	// Caller env present -> forwarded directly + sources ~/.zshrc.
	s1 := buildProxyShellExports(map[string]string{"https_proxy": "http://127.0.0.1:10808"}, "", "", "")
	if !strings.Contains(s1, `source "$HOME/.zshrc"`) || !strings.Contains(s1, `export https_proxy='http://127.0.0.1:10808'`) {
		t.Fatalf("unexpected caller proxy exports: %s", s1)
	}

	// Caller env empty -> fallback conditional export after sourcing ~/.zshrc.
	s2 := buildProxyShellExports(nil, hp, hsp, ap)
	if !strings.Contains(s2, `if [[ -z`) || !strings.Contains(s2, `HTTPS_PROXY='http://127.0.0.1:7890'`) || !strings.Contains(s2, `ALL_PROXY='socks5://127.0.0.1:7891'`) {
		t.Fatalf("unexpected fallback proxy exports: %s", s2)
	}
}
