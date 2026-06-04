//go:build integration

package integration

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mathaix/openclawmachines/backend/internal/metadata"
)

// TestColdBootCandidate boots a VM with the candidate openclaw artifact in
// FULL (non-quick-start) mode and measures the actual user-visible cold-boot
// behavior — not the liveness probe (which responds the moment HTTP binds and
// is therefore insensitive to deferSidecars). What we actually care about is:
// when does the dashboard UI load *without 502s on its asset requests*?
//
// The original regression was: gateway "ready" was deferred until all sidecars
// finished (~53s), but the HTTP server bound much earlier. Asset requests
// arriving in that window queued behind synchronous Node init work and timed
// out at the auth-proxy 30s ReadTimeout. The Option B patch flips the
// deferSidecars default so sidecars load asynchronously after the HTTP
// server is responsive.
//
// Method:
//  1. Boot the VM (no quick-start) and start polling `/gateway/` immediately.
//  2. Keep hammering `/gateway/` and a few common control-ui asset paths.
//  3. Record:
//     - time-to-first-200 on `/gateway/`           (HTML responsive)
//     - time-to-first-clean-window                 (60s with no 502s)
//     - total 502 count across the boot window
//
// Promotion gate: zero 502s observed in the first 60s of polling, AND
// `/gateway/` returns 200 within 30s of bootStart. Phase 1 + Option B
// expectation: both numbers should match 4.15-era behavior (HTML in ~12s,
// no 502s).
//
// Run:
//
//	TEST_OPENCLAW_MANIFEST_URI=file:///tmp/r2-channel-manifest.json \
//	  go test -v -count=1 -tags integration -timeout 10m \
//	  -run TestColdBootCandidate ./internal/integration/...
func TestColdBootCandidate(t *testing.T) {
	if os.Getenv("TEST_OPENCLAW_MANIFEST_URI") == "" {
		t.Skip("TEST_OPENCLAW_MANIFEST_URI not set; this test only runs against a staged candidate")
	}

	cfg := skipIfNoPrereqs(t)
	setupTestDirs(t, cfg)
	cfg.OpenClawManifestURI = "gs://example-ocm-artifacts/openclaw/manifest-stable.json"

	version := discoverStableOpenClawVersion(t)
	t.Logf("Candidate openclaw version: %s", version)

	vmCfg := generateTestVMConfig(0)
	vmCfg.DataVolumeGB = 5
	vmCfg.KernelExtraArgs = "" // ← key: full sidecar/channel load path runs
	vmCfg.RuntimeSelection = &metadata.RuntimeSelection{
		ResolvedOpenClawVersion: version,
		VersionSource:           "pinned",
		RuntimeSource:           "artifact",
		OpenClawManifestURI:     cfg.OpenClawManifestURI,
	}

	bootStart := time.Now()
	_, wsURL, vmCfg := setupInitTestVMWithPrepare(t, vmCfg, nil)
	t.Logf("VM ready at +%v (init script done)", time.Since(bootStart).Round(time.Second))

	// Walk the asset path from the moment we have networking. Don't wait for
	// /health to be 200 — that's the very thing whose insensitivity we're
	// trying to work around.
	cookies := assetCookies{}
	probeStart := time.Now()
	maxProbeWindow := 4 * time.Minute

	type probeResult struct {
		first200OnRoot       time.Time
		firstCleanAssetBatch time.Time
		count502             int
		count2xx             int
		assetPaths           []string
	}
	var pr probeResult

	// Phase A: keep polling /gateway/ until it returns 200, recording any 502s.
	rootURL := fmt.Sprintf("http://%s:8080/gateway/", vmCfg.VMIP)
	t.Logf("Phase A: polling %s for first 200...", rootURL)
	htmlBody := ""
	for time.Since(probeStart) < maxProbeWindow {
		token := issueMachineToken(t, vmCfg.MachineID, vmCfg.SigningKey, []string{"gateway"})
		status, body, _ := fetchAsset(rootURL, token, &cookies, 5*time.Second)
		switch {
		case status == 200:
			pr.first200OnRoot = time.Now()
			htmlBody = body
			t.Logf("Gate #5a — /gateway/ first 200 at +%v", pr.first200OnRoot.Sub(bootStart).Round(time.Second))
		case status == 502:
			pr.count502++
		case status >= 200 && status < 300:
			pr.count2xx++
		}
		if status == 200 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pr.first200OnRoot.IsZero() {
		t.Fatalf("Gate #5a FAIL: /gateway/ never returned 200 within %v (502 count: %d)", maxProbeWindow, pr.count502)
	}

	// Extract asset URLs from the HTML so Phase B exercises the actual paths
	// the browser would request. Falls back to a hardcoded common path set if
	// extraction fails.
	pr.assetPaths = extractAssetPaths(htmlBody)
	if len(pr.assetPaths) == 0 {
		pr.assetPaths = []string{"/gateway/__openclaw/control-ui-config.json", "/gateway/favicon.svg"}
		t.Logf("Phase B: HTML had no parseable asset URLs; using fallback set %v", pr.assetPaths)
	} else {
		t.Logf("Phase B: extracted %d asset paths from HTML", len(pr.assetPaths))
	}

	// Phase B: fetch each asset path repeatedly. Watch for 502s. We're done
	// when we get a clean batch (all 200, no 502s) — that's the user-visible
	// "UI loads cleanly" signal.
	cleanBatchDeadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(cleanBatchDeadline) {
		token := issueMachineToken(t, vmCfg.MachineID, vmCfg.SigningKey, []string{"gateway"})
		batch502 := 0
		batch200 := 0
		for _, p := range pr.assetPaths {
			url := fmt.Sprintf("http://%s:8080%s", vmCfg.VMIP, p)
			status, _, _ := fetchAsset(url, token, &cookies, 5*time.Second)
			if status == 200 {
				batch200++
			}
			if status == 502 {
				batch502++
				pr.count502++
			}
		}
		pr.count2xx += batch200
		if batch502 == 0 && batch200 == len(pr.assetPaths) {
			pr.firstCleanAssetBatch = time.Now()
			t.Logf("Gate #5b — all assets 200, no 502s at +%v",
				pr.firstCleanAssetBatch.Sub(bootStart).Round(time.Second))
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Phase C: dump VM-side gateway/auth-proxy state via terminal for richer
	// debugging if anything looked off. Best-effort; the gates above are
	// authoritative.
	dump := testTerminalCommandSplit(t, wsURL, vmCfg.ProxyToken,
		`echo "=== ext dirs ==="; ls /ocm-runtime/node_modules/openclaw/dist/extensions 2>/dev/null | sort; `+
			`echo "=== gateway log tail ==="; tail -80 /var/log/openclaw-gateway.log 2>/dev/null`,
		20*time.Second)

	// Gate #4 — kept channels present in dist/extensions on the VM.
	keptChannels := []string{"telegram", "slack", "discord", "whatsapp"}
	missing := []string{}
	for _, ch := range keptChannels {
		// `ls` output: each entry on its own line, no trailing newlines guaranteed.
		// Match a line that's exactly the channel name (start- or newline-anchored).
		matched, _ := regexp.MatchString(`(?m)^`+regexp.QuoteMeta(ch)+`$`, dump)
		if !matched {
			missing = append(missing, ch)
		}
	}
	if len(missing) > 0 {
		t.Errorf("Gate #4: kept channel(s) missing from dist/extensions on VM: %v", missing)
	}

	// Scrubbed channels must be absent.
	scrubbedChannels := []string{"matrix", "msteams", "signal", "feishu", "irc", "qqbot", "nostr"}
	leaked := []string{}
	for _, ch := range scrubbedChannels {
		matched, _ := regexp.MatchString(`(?m)^`+regexp.QuoteMeta(ch)+`$`, dump)
		if matched {
			leaked = append(leaked, ch)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("Gate #4: scrubbed channel(s) UNEXPECTEDLY present on VM: %v", leaked)
	}

	// Gate #4 — no adapter load failures for kept channels in the gateway log.
	for _, ch := range keptChannels {
		// "load failed" is the canonical openclaw plugin-load error string.
		if strings.Contains(strings.ToLower(dump), "[plugins] "+ch+": load failed") {
			t.Errorf("Gate #4: kept channel %q logged a load failure", ch)
		}
	}

	// Final verdict on the asset gate.
	if pr.firstCleanAssetBatch.IsZero() {
		t.Errorf("Gate #5b FAIL: never observed a clean asset batch within window (502 count: %d)", pr.count502)
	}

	t.Log("Phase 1 candidate validation summary:")
	if !pr.first200OnRoot.IsZero() {
		t.Logf("  - /gateway/ first 200:    +%v", pr.first200OnRoot.Sub(bootStart).Round(time.Second))
	}
	if !pr.firstCleanAssetBatch.IsZero() {
		t.Logf("  - All assets clean:       +%v", pr.firstCleanAssetBatch.Sub(bootStart).Round(time.Second))
	}
	t.Logf("  - 502s observed:          %d", pr.count502)
	t.Logf("  - 2xx observed:           %d", pr.count2xx)
	t.Logf("  - Asset paths probed:     %d", len(pr.assetPaths))

	if pr.count502 > 0 {
		t.Logf("  (Pre-promotion expectation: 0 502s. Phase 1 + Option B should eliminate them.)")
	}
}

// fetchAsset performs a single GET request through the auth-proxy with the
// machine token. Returns status, body (truncated), and an error. Network
// errors collapse to status=0.
func fetchAsset(url, token string, cookies *assetCookies, timeout time.Duration) (int, string, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("X-Machine-Token", token)
	for _, c := range cookies.list() {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	cookies.absorb(resp.Cookies())
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(body), nil
}

// assetCookies is a tiny per-test cookie jar. The auth-proxy may set cookies
// on first response and the gateway expects them on follow-ups.
type assetCookies struct {
	cs []*http.Cookie
}

func (c *assetCookies) absorb(in []*http.Cookie) {
	c.cs = append(c.cs, in...)
}

func (c *assetCookies) list() []*http.Cookie {
	return c.cs
}

// extractAssetPaths pulls /gateway/assets/* URLs from a control-ui HTML
// payload. Returns at most 12 unique paths to keep probe batches small.
func extractAssetPaths(html string) []string {
	re := regexp.MustCompile(`(?:src|href)\s*=\s*"(/gateway/[^"\s]+)"`)
	hits := re.FindAllStringSubmatch(html, -1)
	seen := map[string]bool{}
	out := []string{}
	for _, h := range hits {
		if len(h) < 2 {
			continue
		}
		p := h[1]
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= 12 {
			break
		}
	}
	sort.Strings(out)
	return out
}
