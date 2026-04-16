package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/getlantern/systray"
)

const appName = "claude-usage-bar"

// ── Data types ──

type UsageData struct {
	UpdatedAt int64          `json:"updated_at"`
	Mode      string         `json:"mode,omitempty"` // "plan" or "apikey"
	FiveHour  RateInfo       `json:"five_hour"`
	SevenDay  RateInfo       `json:"seven_day"`
	Tokens    *TokenRateInfo `json:"tokens,omitempty"`
	Requests  *TokenRateInfo `json:"requests,omitempty"`
	Model     string         `json:"model"`
	SessionID string         `json:"session_id"`
}

func (d *UsageData) isAPIKeyMode() bool {
	return d.Mode == "apikey" || d.Tokens != nil || d.Requests != nil
}

type RateInfo struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"`
}

type TokenRateInfo struct {
	Limit     int64  `json:"limit"`
	Remaining int64  `json:"remaining"`
	ResetsAt  *int64 `json:"resets_at,omitempty"`
}

type StatusLineInput struct {
	RateLimits *struct {
		FiveHour *RateInfo `json:"five_hour"`
		SevenDay *RateInfo `json:"seven_day"`
	} `json:"rate_limits"`
	Model *struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`
	SessionID string `json:"session_id"`
}

// ── Cumulative usage (parsed from ~/.claude/projects/**/*.jsonl) ──

type ModelUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
}

type CumulativeUsage struct {
	ScannedAt    int64                `json:"scanned_at"`
	SinceUnix    int64                `json:"since_unix,omitempty"` // 0 = no filter
	ByModel      map[string]ModelUsage `json:"by_model"`
	TotalCostUSD float64              `json:"total_cost_usd"`
	TotalOutput  int64                `json:"total_output"`
	FileOffsets  map[string]int64     `json:"file_offsets,omitempty"` // path → bytes already parsed
}

var pricingTable = []struct {
	prefix         string
	inputPerM      float64
	outputPerM     float64
	cacheWritePerM float64
	cacheReadPerM  float64
}{
	{"claude-opus-4", 15.0, 75.0, 18.75, 1.50},
	{"claude-3-opus", 15.0, 75.0, 18.75, 1.50},
	{"claude-sonnet-4", 3.0, 15.0, 3.75, 0.30},
	{"claude-3-7-sonnet", 3.0, 15.0, 3.75, 0.30},
	{"claude-3-5-sonnet", 3.0, 15.0, 3.75, 0.30},
	{"claude-3-sonnet", 3.0, 15.0, 3.75, 0.30},
	{"claude-haiku-4", 0.80, 4.0, 1.00, 0.08},
	{"claude-3-5-haiku", 0.80, 4.0, 1.00, 0.08},
	{"claude-3-haiku", 0.25, 1.25, 0.30, 0.03},
}

func lookupPricing(model string) (inputPerM, outputPerM, cacheWritePerM, cacheReadPerM float64) {
	m := strings.ToLower(model)
	for _, p := range pricingTable {
		if strings.HasPrefix(m, p.prefix) {
			return p.inputPerM, p.outputPerM, p.cacheWritePerM, p.cacheReadPerM
		}
	}
	return 3.0, 15.0, 3.75, 0.30 // default: sonnet pricing
}

func modelCostUSD(model string, u ModelUsage) float64 {
	in, out, cw, cr := lookupPricing(model)
	return (float64(u.InputTokens)*in +
		float64(u.OutputTokens)*out +
		float64(u.CacheWriteTokens)*cw +
		float64(u.CacheReadTokens)*cr) / 1_000_000
}

func shortModelName(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case strings.Contains(m, "haiku"):
		return "haiku"
	default:
		if len(model) > 12 {
			return model[:12]
		}
		return model
	}
}

// jsonl entry shapes
type jsonlEntry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"` // ISO 8601, e.g. "2026-03-26T02:53:34.053Z"
	Message   *struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	cumulativeMu     sync.RWMutex
	latestCumulative *CumulativeUsage
)

func getCumulative() *CumulativeUsage {
	cumulativeMu.RLock()
	defer cumulativeMu.RUnlock()
	return latestCumulative
}

func setCumulative(cu *CumulativeUsage) {
	cumulativeMu.Lock()
	latestCumulative = cu
	cumulativeMu.Unlock()
	out, _ := json.Marshal(cu)
	os.MkdirAll(configDir(), 0755)
	os.WriteFile(cumulativeFilePath(), out, 0644)
}

func cumulativeFilePath() string {
	return filepath.Join(configDir(), "cumulative.json")
}

func sinceDateFilePath() string {
	return filepath.Join(configDir(), "since-date")
}

func loadSinceDate() time.Time {
	raw, err := os.ReadFile(sinceDateFilePath())
	if err != nil {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(string(raw)), time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func saveSinceDate(t time.Time) {
	os.MkdirAll(configDir(), 0755)
	os.WriteFile(sinceDateFilePath(), []byte(t.Format("2006-01-02")), 0644)
}

func promptSinceDate(current time.Time) (time.Time, bool) {
	cur := ""
	if !current.IsZero() {
		cur = current.Format("2006-01-02")
	}
	script := fmt.Sprintf(`display dialog "API key 시작 날짜를 입력하세요 (YYYY-MM-DD):" default answer "%s" with title "Claude Usage Bar"`, cur)
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return time.Time{}, false
	}
	s := string(out)
	idx := strings.Index(s, "text returned:")
	if idx < 0 {
		return time.Time{}, false
	}
	dateStr := strings.TrimSpace(s[idx+len("text returned:"):])
	if dateStr == "" {
		return time.Time{}, true // clear filter
	}
	t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func loadCumulativeFromDisk() *CumulativeUsage {
	raw, err := os.ReadFile(cumulativeFilePath())
	if err != nil {
		return nil
	}
	var cu CumulativeUsage
	if err := json.Unmarshal(raw, &cu); err != nil {
		return nil
	}
	return &cu
}

// scanIncremental reads only new bytes from JSONL files that have grown since last scan.
// Pass nil as existing to do a full scan from scratch.
// sinceDate filters entries: only entries with timestamp >= sinceDate are counted.
func scanIncremental(existing *CumulativeUsage, sinceDate time.Time) *CumulativeUsage {
	home, _ := os.UserHomeDir()
	projectsDir := filepath.Join(home, ".claude", "projects")

	var sinceUnix int64
	if !sinceDate.IsZero() {
		sinceUnix = sinceDate.Unix()
	}

	cu := &CumulativeUsage{
		ScannedAt:   time.Now().Unix(),
		SinceUnix:   sinceUnix,
		ByModel:     make(map[string]ModelUsage),
		FileOffsets: make(map[string]int64),
	}
	// Copy existing accumulated data only if the sinceDate hasn't changed
	if existing != nil && existing.SinceUnix == sinceUnix {
		for model, u := range existing.ByModel {
			cu.ByModel[model] = u
		}
		for p, off := range existing.FileOffsets {
			cu.FileOffsets[p] = off
		}
	}
	// If sinceDate changed, we do a fresh full scan (no existing data copied)

	filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		currentSize := info.Size()
		knownOffset := cu.FileOffsets[path]

		if currentSize < knownOffset {
			// File was truncated/replaced — rescan from beginning
			knownOffset = 0
			// Remove stale model counts? We can't easily subtract.
			// Simplest: mark full rescan needed. For now, just re-read from 0.
		}
		if currentSize == knownOffset {
			return nil // nothing new
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		if knownOffset > 0 {
			if _, err := f.Seek(knownOffset, io.SeekStart); err != nil {
				return nil
			}
		}

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for scanner.Scan() {
			var entry jsonlEntry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			if entry.Type != "assistant" || entry.Message == nil || entry.Message.Model == "" || entry.Message.Usage == nil {
				continue
			}
			// Apply since-date filter via timestamp field
			if sinceUnix > 0 && entry.Timestamp != "" {
				ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
				if err == nil && ts.Unix() < sinceUnix {
					continue
				}
			}
			u := entry.Message.Usage
			if u.InputTokens == 0 && u.OutputTokens == 0 {
				continue
			}
			model := strings.ToLower(entry.Message.Model)
			acc := cu.ByModel[model]
			acc.InputTokens += u.InputTokens
			acc.OutputTokens += u.OutputTokens
			acc.CacheWriteTokens += u.CacheCreationInputTokens
			acc.CacheReadTokens += u.CacheReadInputTokens
			cu.ByModel[model] = acc
		}
		// Record how far we've read (use observed size at walk time)
		cu.FileOffsets[path] = currentSize
		return nil
	})

	var totalCost float64
	var totalOutput int64
	for model, u := range cu.ByModel {
		totalCost += modelCostUSD(model, u)
		totalOutput += u.OutputTokens
	}
	cu.TotalCostUSD = totalCost
	cu.TotalOutput = totalOutput

	return cu
}

// cumulativeRescanCh is a 1-element channel used to debounce incremental rescans.
var cumulativeRescanCh = make(chan struct{}, 1)

// triggerCumulativeRescan requests an incremental rescan (non-blocking; drops if one is already pending).
func triggerCumulativeRescan() {
	select {
	case cumulativeRescanCh <- struct{}{}:
	default:
	}
}

func startCumulativeScanner() {
	// Show cached result immediately on startup
	if cu := loadCumulativeFromDisk(); cu != nil {
		setCumulative(cu)
	}
	// Initial full scan in background
	go func() {
		sinceDate := loadSinceDate()
		cu := scanIncremental(loadCumulativeFromDisk(), sinceDate)
		setCumulative(cu)
		refreshUI()
		updateSinceDateMenuItem()
	}()
	// Rescan loop: responds to triggers + periodic fallback
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-cumulativeRescanCh:
				time.Sleep(500 * time.Millisecond) // debounce
				for len(cumulativeRescanCh) > 0 {
					<-cumulativeRescanCh
				}
				sinceDate := loadSinceDate()
				cu := scanIncremental(getCumulative(), sinceDate)
				setCumulative(cu)
				refreshUI()
			case <-ticker.C:
				sinceDate := loadSinceDate()
				cu := scanIncremental(getCumulative(), sinceDate)
				setCumulative(cu)
				refreshUI()
			}
		}
	}()
}

type HistoryEntry struct {
	Display   string `json:"display"`
	Timestamp int64  `json:"timestamp"`
	Project   string `json:"project"`
	SessionID string `json:"sessionId"`
}

type RecentSession struct {
	SessionID    string
	Project      string
	FirstDisplay string
	LastActive   int64
}

// ── Paths ──

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", appName)
}

func usageFilePath() string {
	return filepath.Join(configDir(), "usage.json")
}

// ── PID file ──

func pidFilePath() string {
	return filepath.Join(configDir(), "pid")
}

func isAlreadyRunning() bool {
	raw, err := os.ReadFile(pidFilePath())
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if the process exists without killing it
	return proc.Signal(syscall.Signal(0)) == nil
}

func writePidFile() {
	os.MkdirAll(configDir(), 0755)
	os.WriteFile(pidFilePath(), []byte(strconv.Itoa(os.Getpid())), 0644)
}

func removePidFile() {
	os.Remove(pidFilePath())
}

// ── Entry point ──

const envDaemon = "CLAUDE_USAGE_BAR_DAEMON"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "statusline":
			runStatusLine()
			return
		case "wrap":
			runWrap(os.Args[2:])
			return
		case "setup":
			runSetup()
			return
		case "uninstall":
			runUninstall()
			return
		case "--foreground":
			startWidget()
			return
		case "-h", "--help", "help":
			printHelp()
			return
		default:
			// Process wrapper mode (claudeProcessWrapper)
			args := os.Args[1:]
			if isExecutablePath(args[0]) {
				// Normal wrapper call: claude-usage-bar /path/to/claude [args...]
				runWrap(args)
			} else {
				// Extension called with flags directly: claude-usage-bar --auth-status
				// Prepend the real claude binary path
				runWrap(append([]string{findClaudeBinary()}, args...))
			}
			return
		}
	}

	// Already running as daemon child — start the widget
	if os.Getenv(envDaemon) == "1" {
		startWidget()
		return
	}

	// Check if already running
	if isAlreadyRunning() {
		fmt.Println("claude-usage-bar is already running.")
		return
	}

	// Fork to background and exit the parent
	bin := stableBinPath()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), envDaemon+"=1")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Failed to start in background:", err)
		os.Exit(1)
	}
	fmt.Println("claude-usage-bar started (pid", cmd.Process.Pid, ")")
}

func startWidget() {
	if isAlreadyRunning() {
		fmt.Println("claude-usage-bar is already running.")
		return
	}
	writePidFile()
	port := startPersistentProxy()
	ensureSetup()
	if port > 0 {
		if err := setupProxyEnv(port); err != nil {
			fmt.Fprintln(os.Stderr, "Proxy env setup failed:", err)
		}
	}
	systray.Run(onReady, onExit)
}

func printHelp() {
	fmt.Printf(`%s — Claude Code usage monitor for macOS menu bar

Usage:
  %s              Launch the menu bar widget (backgrounds automatically)
  %s --foreground Launch in foreground (for debugging)
  %s statusline   StatusLine handler (used by Claude Code CLI)
  %s wrap <cmd>   Process wrapper (used by VS Code extensions)
  %s setup        Auto-configure Claude Code CLI and VS Code extensions
  %s uninstall    Remove all config, LaunchAgent, and statusLine settings
`, appName, appName, appName, appName, appName, appName, appName)
}

func isExecutablePath(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0111 != 0
}

func findClaudeBinary() string {
	ourPath := stableBinPath()
	// Search PATH for "claude" binary, excluding ourselves
	for _, dir := range strings.Split(os.Getenv("PATH"), ":") {
		candidate := filepath.Join(dir, "claude")
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if abs == ourPath {
			continue
		}
		if isExecutablePath(abs) {
			return abs
		}
	}
	// Fallback: standard location
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin", "claude")
}

// ── StatusLine subcommand ──

func runStatusLine() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Println("")
		return
	}

	var sl StatusLineInput
	if err := json.Unmarshal(input, &sl); err != nil {
		fmt.Println("")
		return
	}

	// Merge with existing data to preserve API key token info written by proxy
	var data UsageData
	if existing, err := loadUsage(); err == nil {
		data = *existing
	}
	data.UpdatedAt = time.Now().Unix()
	data.SessionID = sl.SessionID

	if sl.Model != nil {
		data.Model = sl.Model.DisplayName
	}
	if sl.RateLimits != nil {
		data.Mode = "plan"
		if sl.RateLimits.FiveHour != nil {
			data.FiveHour = *sl.RateLimits.FiveHour
		}
		if sl.RateLimits.SevenDay != nil {
			data.SevenDay = *sl.RateLimits.SevenDay
		}
		data.Tokens = nil
		data.Requests = nil
	} else {
		// No plan rate limits → API key mode; clear stale plan data
		data.Mode = "apikey"
		data.FiveHour = RateInfo{}
		data.SevenDay = RateInfo{}
	}

	dir := configDir()
	os.MkdirAll(dir, 0755)

	out, _ := json.Marshal(data)
	os.WriteFile(usageFilePath(), out, 0644)

	fmt.Println("")
}

// ── Wrap subcommand (process wrapper for VS Code extensions) ──

func runWrap(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: claude-usage-bar wrap <command> [args...]")
		os.Exit(1)
	}

	// Start local reverse proxy to intercept rate limit headers
	proxyResult, proxyErr := startRateLimitProxy()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Inject proxy env vars into child process
	env := os.Environ()
	if proxyErr == nil && proxyResult != nil {
		env = setEnv(env, "ANTHROPIC_BASE_URL", fmt.Sprintf("https://127.0.0.1:%d", proxyResult.port))
		env = setEnv(env, "NODE_EXTRA_CA_CERTS", proxyResult.certFile)
	}
	cmd.Env = env

	// Clean up temp cert file on exit
	if proxyResult != nil && proxyResult.certFile != "" {
		defer os.Remove(proxyResult.certFile)
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "wrap: start:", err)
		os.Exit(1)
	}

	// Forward signals to child process
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			cmd.Process.Signal(sig)
		}
	}()

	cmd.Wait()
	if cmd.ProcessState != nil {
		os.Exit(cmd.ProcessState.ExitCode())
	}
}

// setEnv replaces or appends an environment variable.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// ── Persistent HTTP proxy (CLI mode) ──

func proxyPortFilePath() string {
	return filepath.Join(configDir(), "proxy-port")
}

func loadSavedProxyPort() int {
	raw, err := os.ReadFile(proxyPortFilePath())
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return port
}

// startPersistentProxy starts an HTTP proxy that forwards to api.anthropic.com.
// Listening side is plain HTTP (localhost only) so no TLS cert is needed.
// Claude Code is configured to use it via ANTHROPIC_BASE_URL in settings.json.
func startPersistentProxy() int {
	port := loadSavedProxyPort()
	if port == 0 {
		port = 18765
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		// Saved port is busy — try a random free port
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0
		}
	}
	port = listener.Addr().(*net.TCPAddr).Port

	os.MkdirAll(configDir(), 0755)
	os.WriteFile(proxyPortFilePath(), []byte(strconv.Itoa(port)), 0644)

	target, _ := url.Parse("https://api.anthropic.com")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1

	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = target.Host
		req.URL.Scheme = "https"
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		writeRateLimitsFromHeaders(resp.Header)
		return nil
	}

	go http.Serve(listener, proxy)
	return port
}

// setupProxyEnv writes ANTHROPIC_BASE_URL to ~/.claude/settings.json env block.
func setupProxyEnv(port int) error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	var settings map[string]interface{}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		settings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("error parsing %s: %v", settingsPath, err)
		}
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Check if already configured correctly
	if envMap, ok := settings["env"].(map[string]interface{}); ok {
		if v, ok := envMap["ANTHROPIC_BASE_URL"].(string); ok && v == baseURL {
			return nil
		}
	}

	if settings["env"] == nil {
		settings["env"] = make(map[string]interface{})
	}
	envMap := settings["env"].(map[string]interface{})
	envMap["ANTHROPIC_BASE_URL"] = baseURL

	dir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("error creating directory %s: %v", dir, err)
	}
	out, _ := json.MarshalIndent(settings, "", "  ")
	return os.WriteFile(settingsPath, out, 0644)
}

// ── Rate limit reverse proxy ──

var proxyUsageMu sync.Mutex

// proxyResult holds the proxy startup result including the CA cert path for NODE_EXTRA_CA_CERTS.
type proxyResult struct {
	port     int
	certFile string // temp file with PEM-encoded CA cert
}

func startRateLimitProxy() (*proxyResult, error) {
	cert, certPEM, err := generateSelfSignedCert()
	if err != nil {
		return nil, fmt.Errorf("cert: %w", err)
	}

	// Write CA cert to temp file for NODE_EXTRA_CA_CERTS
	certFile, err := os.CreateTemp("", "claude-usage-bar-ca-*.pem")
	if err != nil {
		return nil, fmt.Errorf("temp cert: %w", err)
	}
	if _, err := certFile.Write(certPEM); err != nil {
		os.Remove(certFile.Name())
		return nil, fmt.Errorf("write cert: %w", err)
	}
	certFile.Close()

	target, _ := url.Parse("https://api.anthropic.com")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // Flush immediately for SSE streaming

	// Fix Host header for upstream
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = target.Host
	}

	// Extract rate limit headers from responses
	proxy.ModifyResponse = func(resp *http.Response) error {
		writeRateLimitsFromHeaders(resp.Header)
		return nil
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsConfig)
	if err != nil {
		os.Remove(certFile.Name())
		return nil, fmt.Errorf("listen: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	go http.Serve(listener, proxy)
	return &proxyResult{port: port, certFile: certFile.Name()}, nil
}

func writeRateLimitsFromHeaders(h http.Header) {
	// Pro/Team plan: unified rate limit headers
	util5h := h.Get("anthropic-ratelimit-unified-5h-utilization")
	reset5h := h.Get("anthropic-ratelimit-unified-5h-reset")
	util7d := h.Get("anthropic-ratelimit-unified-7d-utilization")
	reset7d := h.Get("anthropic-ratelimit-unified-7d-reset")

	// API key: token/request rate limit headers
	tokensLimitStr := h.Get("anthropic-ratelimit-tokens-limit")
	tokensRemainingStr := h.Get("anthropic-ratelimit-tokens-remaining")
	tokensResetStr := h.Get("anthropic-ratelimit-tokens-reset")
	requestsLimitStr := h.Get("anthropic-ratelimit-requests-limit")
	requestsRemainingStr := h.Get("anthropic-ratelimit-requests-remaining")
	requestsResetStr := h.Get("anthropic-ratelimit-requests-reset")

	hasPlanHeaders := util5h != "" || util7d != ""
	hasAPIKeyHeaders := tokensLimitStr != "" || tokensRemainingStr != ""

	if !hasPlanHeaders && !hasAPIKeyHeaders {
		return
	}

	proxyUsageMu.Lock()
	defer proxyUsageMu.Unlock()

	var data UsageData
	if existing, err := loadUsage(); err == nil {
		data = *existing
	}
	data.UpdatedAt = time.Now().Unix()

	if hasPlanHeaders {
		data.Mode = "plan"
		data.Tokens = nil
		data.Requests = nil

		if v, err := strconv.ParseFloat(util5h, 64); err == nil {
			pct := math.Round(v * 10000) / 100
			data.FiveHour.UsedPercentage = &pct
		}
		if v, err := strconv.ParseInt(reset5h, 10, 64); err == nil {
			data.FiveHour.ResetsAt = &v
		}
		if v, err := strconv.ParseFloat(util7d, 64); err == nil {
			pct := math.Round(v * 10000) / 100
			data.SevenDay.UsedPercentage = &pct
		}
		if v, err := strconv.ParseInt(reset7d, 10, 64); err == nil {
			data.SevenDay.ResetsAt = &v
		}
	} else {
		data.Mode = "apikey"
		data.FiveHour = RateInfo{}
		data.SevenDay = RateInfo{}

		if tokensLimitStr != "" || tokensRemainingStr != "" {
			if data.Tokens == nil {
				data.Tokens = &TokenRateInfo{}
			}
			if v, err := strconv.ParseInt(tokensLimitStr, 10, 64); err == nil {
				data.Tokens.Limit = v
			}
			if v, err := strconv.ParseInt(tokensRemainingStr, 10, 64); err == nil {
				data.Tokens.Remaining = v
			}
			if t, err := time.Parse(time.RFC3339, tokensResetStr); err == nil {
				ts := t.Unix()
				data.Tokens.ResetsAt = &ts
			}
		}

		if requestsLimitStr != "" || requestsRemainingStr != "" {
			if data.Requests == nil {
				data.Requests = &TokenRateInfo{}
			}
			if v, err := strconv.ParseInt(requestsLimitStr, 10, 64); err == nil {
				data.Requests.Limit = v
			}
			if v, err := strconv.ParseInt(requestsRemainingStr, 10, 64); err == nil {
				data.Requests.Remaining = v
			}
			if t, err := time.Parse(time.RFC3339, requestsResetStr); err == nil {
				ts := t.Unix()
				data.Requests.ResetsAt = &ts
			}
		}
	}

	dir := configDir()
	os.MkdirAll(dir, 0755)
	out, _ := json.Marshal(data)
	os.WriteFile(usageFilePath(), out, 0644)
}

func generateSelfSignedCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{appName}},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * 365 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tlsCert, certPEM, nil
}

// ── Setup ──

// setupStatusLine configures ~/.claude/settings.json and returns any error.
func setupStatusLine() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	// Resolve full path to the binary
	binPath, err := exec.LookPath(os.Args[0])
	if err != nil {
		binPath = os.Args[0]
	}
	binPath, _ = filepath.Abs(binPath)
	command := binPath + " statusline"

	// Read existing settings
	var settings map[string]interface{}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		settings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("error parsing %s: %v", settingsPath, err)
		}
	}

	// Check if already configured
	if sl, ok := settings["statusLine"].(map[string]interface{}); ok {
		if cmd, ok := sl["command"].(string); ok && cmd == command {
			return nil
		}
	}

	// Set statusLine
	settings["statusLine"] = map[string]string{
		"type":    "command",
		"command": command,
	}

	// Write back
	dir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("error creating directory %s: %v", dir, err)
	}
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(settingsPath, out, 0644); err != nil {
		return fmt.Errorf("error writing %s: %v", settingsPath, err)
	}

	return nil
}

// setupProcessWrapper configures claudeCode.claudeProcessWrapper in VS Code settings.
func setupProcessWrapper() []string {
	home, _ := os.UserHomeDir()
	binPath := stableBinPath()

	// VS Code variants and their settings paths
	editors := []struct {
		name string
		path string
	}{
		{"VS Code", filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json")},
		{"Cursor", filepath.Join(home, "Library", "Application Support", "Cursor", "User", "settings.json")},
		{"Antigravity", filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "settings.json")},
	}

	var configured []string
	for _, editor := range editors {
		// Skip if the editor's User directory doesn't exist (not installed)
		if _, err := os.Stat(filepath.Dir(editor.path)); os.IsNotExist(err) {
			continue
		}

		var settings map[string]interface{}
		raw, err := os.ReadFile(editor.path)
		if err != nil {
			settings = make(map[string]interface{})
		} else {
			if err := json.Unmarshal(raw, &settings); err != nil {
				continue
			}
		}

		// Check if already configured
		if v, ok := settings["claudeCode.claudeProcessWrapper"].(string); ok && v == binPath {
			configured = append(configured, editor.name)
			continue
		}

		settings["claudeCode.claudeProcessWrapper"] = binPath
		out, _ := json.MarshalIndent(settings, "", "  ")
		if err := os.WriteFile(editor.path, out, 0644); err != nil {
			continue
		}
		configured = append(configured, editor.name)
	}
	return configured
}

// ensureSetup runs setup silently on every app launch.
func ensureSetup() {
	if err := setupStatusLine(); err != nil {
		fmt.Fprintln(os.Stderr, "Auto-setup failed:", err)
	}
	setupProcessWrapper()
}

// runSetup is the CLI entrypoint for `claude-usage-bar setup`.
func runSetup() {
	if err := setupStatusLine(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		home, _ := os.UserHomeDir()
		settingsPath := filepath.Join(home, ".claude", "settings.json")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Please add the following to", settingsPath, "manually:")
		fmt.Fprintf(os.Stderr, "  \"statusLine\": { \"type\": \"command\", \"command\": \"%s statusline\" }\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "If you see 'operation not permitted', check macOS Privacy & Security > Full Disk Access.")
		os.Exit(1)
	}
	home, _ := os.UserHomeDir()
	fmt.Println("✓ Configured statusLine in", filepath.Join(home, ".claude", "settings.json"))

	editors := setupProcessWrapper()
	if len(editors) > 0 {
		fmt.Println("✓ Configured processWrapper for", strings.Join(editors, ", "))
	}
}

// ── Uninstall subcommand ──

func runUninstall() {
	fmt.Println("Uninstalling", appName+"...")

	// 1. Remove LaunchAgent
	if isLaunchAgentInstalled() {
		// Unload the agent first
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), launchAgentPath()).Run()
		if err := removeLaunchAgent(); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Failed to remove LaunchAgent: %v\n", err)
		} else {
			fmt.Println("  ✓ Removed LaunchAgent")
		}
	} else {
		fmt.Println("  - LaunchAgent not found (skipped)")
	}

	// 2. Remove statusLine and proxy env from ~/.claude/settings.json
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if raw, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if err := json.Unmarshal(raw, &settings); err == nil {
			changed := false
			if sl, ok := settings["statusLine"].(map[string]interface{}); ok {
				if cmd, ok := sl["command"].(string); ok && strings.Contains(cmd, appName) {
					delete(settings, "statusLine")
					changed = true
					fmt.Println("  ✓ Removed statusLine from", settingsPath)
				}
			}
			if envMap, ok := settings["env"].(map[string]interface{}); ok {
				if _, exists := envMap["ANTHROPIC_BASE_URL"]; exists {
					delete(envMap, "ANTHROPIC_BASE_URL")
					if len(envMap) == 0 {
						delete(settings, "env")
					}
					changed = true
					fmt.Println("  ✓ Removed ANTHROPIC_BASE_URL from", settingsPath)
				}
			}
			if changed {
				out, _ := json.MarshalIndent(settings, "", "  ")
				if err := os.WriteFile(settingsPath, out, 0644); err != nil {
					fmt.Fprintf(os.Stderr, "  ✗ Failed to update %s: %v\n", settingsPath, err)
				}
			}
		}
	} else {
		fmt.Println("  - Settings file not found (skipped)")
	}

	// 3. Remove claudeProcessWrapper from VS Code settings
	for _, editor := range []struct {
		name string
		path string
	}{
		{"VS Code", filepath.Join(home, "Library", "Application Support", "Code", "User", "settings.json")},
		{"Cursor", filepath.Join(home, "Library", "Application Support", "Cursor", "User", "settings.json")},
		{"Antigravity", filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "settings.json")},
	} {
		if raw, err := os.ReadFile(editor.path); err == nil {
			var settings map[string]interface{}
			if err := json.Unmarshal(raw, &settings); err == nil {
				if v, ok := settings["claudeCode.claudeProcessWrapper"].(string); ok && strings.Contains(v, appName) {
					delete(settings, "claudeCode.claudeProcessWrapper")
					out, _ := json.MarshalIndent(settings, "", "  ")
					if err := os.WriteFile(editor.path, out, 0644); err == nil {
						fmt.Println("  ✓ Removed processWrapper from", editor.name)
					}
				}
			}
		}
	}

	// 4. Remove config directory
	cfgDir := configDir()
	if _, err := os.Stat(cfgDir); err == nil {
		if err := os.RemoveAll(cfgDir); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Failed to remove %s: %v\n", cfgDir, err)
		} else {
			fmt.Println("  ✓ Removed", cfgDir)
		}
	} else {
		fmt.Println("  - Config directory not found (skipped)")
	}

	fmt.Println("Done.")
}

// ── Recent sessions ──

func historyFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "history.jsonl")
}

func loadRecentSessions(limit int) []RecentSession {
	f, err := os.Open(historyFilePath())
	if err != nil {
		return nil
	}
	defer f.Close()

	// Track per-session: first display and last timestamp
	type sessionAcc struct {
		project      string
		firstDisplay string
		firstTS      int64
		lastTS       int64
	}
	sessions := make(map[string]*sessionAcc)
	var order []string

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var e HistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.SessionID == "" || e.Display == "" {
			continue
		}
		// Skip slash commands and short noise
		if strings.HasPrefix(e.Display, "/") || e.Display == "exit" {
			// Still update lastTS
			if s, ok := sessions[e.SessionID]; ok {
				if e.Timestamp > s.lastTS {
					s.lastTS = e.Timestamp
				}
			}
			continue
		}

		if s, ok := sessions[e.SessionID]; ok {
			if e.Timestamp > s.lastTS {
				s.lastTS = e.Timestamp
			}
		} else {
			sessions[e.SessionID] = &sessionAcc{
				project:      e.Project,
				firstDisplay: e.Display,
				firstTS:      e.Timestamp,
				lastTS:       e.Timestamp,
			}
			order = append(order, e.SessionID)
		}
	}

	// Sort by lastTS descending (most recent first)
	// Simple selection sort for small N
	for i := 0; i < len(order); i++ {
		maxIdx := i
		for j := i + 1; j < len(order); j++ {
			if sessions[order[j]].lastTS > sessions[order[maxIdx]].lastTS {
				maxIdx = j
			}
		}
		order[i], order[maxIdx] = order[maxIdx], order[i]
	}

	var result []RecentSession
	for _, sid := range order {
		if len(result) >= limit {
			break
		}
		s := sessions[sid]
		result = append(result, RecentSession{
			SessionID:    sid,
			Project:      s.project,
			FirstDisplay: s.firstDisplay,
			LastActive:   s.lastTS,
		})
	}
	return result
}

func projectName(fullPath string) string {
	return filepath.Base(fullPath)
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

func copyResumeCommand(sessionID, project string) {
	cmd := fmt.Sprintf("cd %s && claude --resume %s", project, sessionID)
	p := exec.Command("pbcopy")
	p.Stdin = strings.NewReader(cmd)
	p.Run()
}

// ── Menu bar widget ──

var (
	m5hLabel *systray.MenuItem
	m5hBar   *systray.MenuItem
	m5hReset *systray.MenuItem

	m7dLabel *systray.MenuItem
	m7dBar   *systray.MenuItem
	m7dReset *systray.MenuItem

	mStatus    *systray.MenuItem
	mSinceDate *systray.MenuItem

	mSessionItems []*systray.MenuItem
)

const (
	barWidth        = 20
	maxSessions     = 5
	sessionLabelLen = 20
)

func onReady() {
	systray.SetTitle("[ 5h --  ·  7d -- ]")
	systray.SetTooltip("Claude Usage Bar")

	m5hLabel = systray.AddMenuItem("", "")
	m5hBar = systray.AddMenuItem("", "")
	m5hReset = systray.AddMenuItem("", "")

	systray.AddSeparator()

	m7dLabel = systray.AddMenuItem("", "")
	m7dBar = systray.AddMenuItem("", "")
	m7dReset = systray.AddMenuItem("", "")

	systray.AddSeparator()

	mStatus = systray.AddMenuItem("", "")
	mSinceDate = systray.AddMenuItem("", "Since: all time  (click to set)")

	systray.AddSeparator()

	// Recent sessions
	systray.AddMenuItem("Recent Sessions", "").Disable()
	for i := 0; i < maxSessions; i++ {
		item := systray.AddMenuItem("", "")
		item.Hide()
		mSessionItems = append(mSessionItems, item)
	}

	systray.AddSeparator()

	mLaunch := systray.AddMenuItem("Launch at Login", "Toggle launch at login")
	if isLaunchAgentInstalled() {
		mLaunch.Check()
	}

	mQuit := systray.AddMenuItem("Quit", "")

	setInactive()
	refreshUI()
	refreshSessions()

	go watchFile()
	go periodicRefresh()
	startCumulativeScanner()

	go func() {
		for {
			select {
			case <-mSinceDate.ClickedCh:
				current := loadSinceDate()
				newDate, ok := promptSinceDate(current)
				if !ok {
					break
				}
				saveSinceDate(newDate)
				updateSinceDateMenuItem()
				// Force full rescan with new date
				go func() {
					cu := scanIncremental(nil, newDate)
					setCumulative(cu)
					refreshUI()
				}()
			case <-mLaunch.ClickedCh:
				if isLaunchAgentInstalled() {
					removeLaunchAgent()
					mLaunch.Uncheck()
				} else {
					installLaunchAgent()
					mLaunch.Check()
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func updateSinceDateMenuItem() {
	if mSinceDate == nil {
		return
	}
	d := loadSinceDate()
	if d.IsZero() {
		mSinceDate.SetTitle("Since: all time  (click to set)")
	} else {
		mSinceDate.SetTitle(fmt.Sprintf("Since: %s  (click to change)", d.Format("2006-01-02")))
	}
}

var currentSessions []RecentSession

func refreshSessions() {
	currentSessions = loadRecentSessions(maxSessions)
	for i := 0; i < maxSessions; i++ {
		if i < len(currentSessions) {
			s := currentSessions[i]
			proj := projectName(s.Project)
			label := fmt.Sprintf("[%s] %s", proj, truncate(s.FirstDisplay, sessionLabelLen))
			mSessionItems[i].SetTitle(label)
			mSessionItems[i].SetTooltip(s.FirstDisplay)
			mSessionItems[i].Show()
			// Start click handler for this item
			go handleSessionClick(i)
		} else {
			mSessionItems[i].Hide()
		}
	}
}

func handleSessionClick(idx int) {
	<-mSessionItems[idx].ClickedCh
	if idx < len(currentSessions) {
		s := currentSessions[idx]
		copyResumeCommand(s.SessionID, s.Project)
	}
	go handleSessionClick(idx)
}

func onExit() {
	removePidFile()
}

func watchFile() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer watcher.Close()

	dir := configDir()
	os.MkdirAll(dir, 0755)
	watcher.Add(dir)

	for {
		select {
		case ev := <-watcher.Events:
			if ev.Name == usageFilePath() && (ev.Op&(fsnotify.Write|fsnotify.Create)) != 0 {
				time.Sleep(50 * time.Millisecond)
				refreshUI()
				triggerCumulativeRescan()
			}
		case <-watcher.Errors:
		}
	}
}

func periodicRefresh() {
	for {
		time.Sleep(30 * time.Second)
		refreshUI()
		refreshSessions()
	}
}

func refreshUI() {
	d, err := loadUsage()
	if err != nil {
		setInactive()
		return
	}

	staleness := time.Since(time.Unix(d.UpdatedAt, 0))
	if staleness > 10*time.Minute {
		setStale(d, staleness)
		return
	}

	setActive(d)
}

func setActive(d *UsageData) {
	if d.isAPIKeyMode() {
		setActiveAPIKey(d)
	} else {
		setActivePlan(d)
	}
	ago := fmtAgo(time.Since(time.Unix(d.UpdatedAt, 0)))
	mStatus.SetTitle(fmt.Sprintf("%s · %s", d.Model, ago))
}

func setActivePlan(d *UsageData) {
	s := pct(d.FiveHour.UsedPercentage)
	w := pct(d.SevenDay.UsedPercentage)
	systray.SetTitle(fmt.Sprintf("[ 5h %s  ·  7d %s ]", s, w))
	m5hLabel.SetTitle(fmt.Sprintf("5h Session                           %s used", s))
	m5hBar.SetTitle(bar(d.FiveHour.UsedPercentage))
	m5hReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.FiveHour.ResetsAt)))
	m7dLabel.SetTitle(fmt.Sprintf("7d All Models                        %s used", w))
	m7dBar.SetTitle(bar(d.SevenDay.UsedPercentage))
	m7dReset.SetTitle(fmt.Sprintf("Resets %s", resetDate(d.SevenDay.ResetsAt)))
}

func setActiveAPIKey(d *UsageData) {
	cu := getCumulative()
	if cu == nil || cu.TotalCostUSD < 0.005 {
		systray.SetTitle("[ scanning… ]")
		m5hLabel.SetTitle("Scanning usage data…")
		m5hBar.SetTitle("")
		m5hReset.SetTitle("")
		m7dLabel.SetTitle("")
		m7dBar.SetTitle("")
		m7dReset.SetTitle("")
		return
	}

	systray.SetTitle(fmt.Sprintf("[ %s ]", fmtCost(cu.TotalCostUSD)))

	// Totals row
	var totalIn, totalOut, totalCacheR int64
	for _, u := range cu.ByModel {
		totalIn += u.InputTokens
		totalOut += u.OutputTokens
		totalCacheR += u.CacheReadTokens
	}
	m5hLabel.SetTitle(fmt.Sprintf("Total Cost              %s", fmtCost(cu.TotalCostUSD)))
	m5hBar.SetTitle(fmt.Sprintf("Output %-6s  Input %-6s  Cache %s",
		fmtTokensM(totalOut), fmtTokensM(totalIn), fmtTokensM(totalCacheR)))
	ago := fmtAgo(time.Since(time.Unix(cu.ScannedAt, 0)))
	m5hReset.SetTitle(fmt.Sprintf("Scanned %s", ago))

	// Per-model breakdown sorted by cost
	type entry struct {
		name string
		cost float64
		out  int64
	}
	var entries []entry
	for model, u := range cu.ByModel {
		entries = append(entries, entry{shortModelName(model), modelCostUSD(model, u), u.OutputTokens})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].cost > entries[j].cost })

	rows := []*systray.MenuItem{m7dLabel, m7dBar, m7dReset}
	for i, row := range rows {
		if i < len(entries) {
			e := entries[i]
			row.SetTitle(fmt.Sprintf("  %-8s  %-9s  %s", e.name, fmtTokensM(e.out)+" out", fmtCost(e.cost)))
		} else {
			row.SetTitle("")
		}
	}
}

func setStale(d *UsageData, staleness time.Duration) {
	if d.isAPIKeyMode() {
		setActiveAPIKey(d)
	} else {
		setActivePlan(d)
	}
	systray.SetTitle("[ ⏸ ]")
	mStatus.SetTitle(fmt.Sprintf("%s · inactive %s", d.Model, fmtAgo(staleness)))
}

func setInactive() {
	systray.SetTitle("[ ⏸ ]")

	m5hLabel.SetTitle("5h Session                            --")
	m5hBar.SetTitle(strings.Repeat("░", barWidth))
	m5hReset.SetTitle(" ")

	m7dLabel.SetTitle("7d All Models                         --")
	m7dBar.SetTitle(strings.Repeat("░", barWidth))
	m7dReset.SetTitle(" ")

	mStatus.SetTitle("Waiting for Claude Code...")
}

// ── Helpers ──

func pct(p *float64) string {
	if p == nil {
		return "--"
	}
	return fmt.Sprintf("%.0f%%", *p)
}

func bar(p *float64) string {
	if p == nil {
		return strings.Repeat("░", barWidth)
	}
	filled := int(*p / 100 * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}

func fmtK(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func fmtTokensM(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.0fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func fmtCost(usd float64) string {
	if usd < 0.005 {
		return "<$0.01"
	}
	if usd < 10 {
		return fmt.Sprintf("$%.2f", usd)
	}
	if usd < 1000 {
		return fmt.Sprintf("$%.0f", usd)
	}
	return fmt.Sprintf("$%.1fk", usd/1000)
}

func tokenAbsolute(t *TokenRateInfo) string {
	if t == nil || t.Limit == 0 {
		return "--"
	}
	used := t.Limit - t.Remaining
	return fmt.Sprintf("%s / %s used  (%s remaining)", fmtK(used), fmtK(t.Limit), fmtK(t.Remaining))
}

func tokenUsedPct(t *TokenRateInfo) string {
	if t == nil || t.Limit == 0 {
		return "--"
	}
	used := float64(t.Limit-t.Remaining) / float64(t.Limit) * 100
	return fmt.Sprintf("%.0f%%", used)
}

func barFromTokenRatio(t *TokenRateInfo) string {
	if t == nil || t.Limit == 0 {
		return strings.Repeat("░", barWidth)
	}
	usedPct := float64(t.Limit-t.Remaining) / float64(t.Limit) * 100
	return bar(&usedPct)
}

func tokenResetDate(t *TokenRateInfo) string {
	if t == nil || t.ResetsAt == nil {
		return "--"
	}
	return resetDate(t.ResetsAt)
}

func resetDate(ts *int64) string {
	if ts == nil {
		return "--"
	}
	t := time.Unix(*ts, 0)
	return t.Format("01/02 15:04")
}

func fmtAgo(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm ago", int(d.Hours()), int(d.Minutes())%60)
}

// ── LaunchAgent ──

const launchAgentLabel = "com.github.hwayoungjun.claude-usage-bar"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func isLaunchAgentInstalled() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

func stableBinPath() string {
	// Prefer the PATH-based path (e.g. /opt/homebrew/bin/claude-usage-bar)
	// which is a stable symlink that survives brew upgrades.
	// os.Executable() resolves symlinks on macOS, returning the Cellar path
	// which breaks after brew upgrade.
	if p, err := exec.LookPath(appName); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	// Fallback to the resolved executable path
	binPath, _ := os.Executable()
	binPath, _ = filepath.Abs(binPath)
	return binPath
}

func installLaunchAgent() error {
	binPath := stableBinPath()

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>--foreground</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
</dict>
</plist>
`, launchAgentLabel, binPath)

	dir := filepath.Dir(launchAgentPath())
	os.MkdirAll(dir, 0755)
	return os.WriteFile(launchAgentPath(), []byte(plist), 0644)
}

func removeLaunchAgent() error {
	return os.Remove(launchAgentPath())
}

func loadUsage() (*UsageData, error) {
	raw, err := os.ReadFile(usageFilePath())
	if err != nil {
		return nil, err
	}
	var d UsageData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.UpdatedAt == 0 {
		return nil, fmt.Errorf("no data")
	}
	return &d, nil
}
