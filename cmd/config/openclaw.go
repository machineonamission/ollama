package config

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
	"github.com/ollama/ollama/types/model"
	"golang.org/x/mod/semver"
)

const defaultGatewayPort = 18789

type Openclaw struct{}

func (c *Openclaw) String() string { return "OpenClaw" }

func (c *Openclaw) Run(model string, args []string) error {
	bin, err := ensureOpenclawInstalled()
	if err != nil {
		return err
	}

	if !IntegrationOnboarded("openclaw") {
		fmt.Fprintf(os.Stderr, "\n%s┌ Security%s\n", ansiBold, ansiReset)
		fmt.Fprintf(os.Stderr, "%s│%s\n", ansiGray, ansiReset)
		fmt.Fprintf(os.Stderr, "%s│%s  OpenClaw can read files and run actions when tools are enabled.\n", ansiGray, ansiReset)
		fmt.Fprintf(os.Stderr, "%s│%s  A bad prompt can trick it into doing unsafe things.\n", ansiGray, ansiReset)
		fmt.Fprintf(os.Stderr, "%s│%s\n", ansiGray, ansiReset)
		fmt.Fprintf(os.Stderr, "%s│%s  Learn more: https://docs.openclaw.ai/gateway/security\n", ansiGray, ansiReset)
		fmt.Fprintf(os.Stderr, "%s└%s\n\n", ansiGray, ansiReset)

		ok, err := confirmPrompt("I understand the risks. Continue?")
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		if err := SetIntegrationOnboarded("openclaw"); err != nil {
			return fmt.Errorf("failed to save onboarding state: %w", err)
		}
	}

	if !c.onboarded() {
		fmt.Fprintf(os.Stderr, "\n%sSetting up OpenClaw with Ollama...%s\n", ansiGreen, ansiReset)
		fmt.Fprintf(os.Stderr, "%s  Model: %s%s\n\n", ansiGray, model, ansiReset)

		cmd := exec.Command(bin, "onboard",
			"--non-interactive",
			"--accept-risk",
			"--auth-choice", "skip",
			"--gateway-token", "ollama",
			"--install-daemon",
			"--skip-channels",
			"--skip-skills",
		)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return windowsHint(fmt.Errorf("openclaw onboarding failed: %w\n\nTry running: openclaw onboard", err))
		}
	}

	token, port := c.gatewayInfo()
	addr := fmt.Sprintf("localhost:%d", port)

	// If the gateway isn't already running (e.g. via the daemon),
	// start it as a background child process.
	if !portOpen(addr) {
		gw := exec.Command(bin, "gateway", "run", "--force")
		if err := gw.Start(); err != nil {
			return windowsHint(fmt.Errorf("failed to start gateway: %w", err))
		}
		defer func() {
			if gw.Process != nil {
				_ = gw.Process.Kill()
				_ = gw.Wait()
			}
		}()
	}

	// Wait for gateway to accept connections.
	if !waitForPort(addr, 30*time.Second) {
		return windowsHint(fmt.Errorf("gateway did not start on %s", addr))
	}

	printOpenclawReady(bin, token, port)

	// Drop user into the TUI. On first launch, trigger the bootstrap
	// ritual with an initial message (matches openclaw's own onboarding).
	tuiArgs := []string{"tui"}
	if c.hasBootstrap() {
		tuiArgs = append(tuiArgs, "--message", "Wake up, my friend!")
	}
	tui := exec.Command(bin, tuiArgs...)
	tui.Stdin = os.Stdin
	tui.Stdout = os.Stdout
	tui.Stderr = os.Stderr
	if err := tui.Run(); err != nil {
		return windowsHint(err)
	}
	return nil
}

// gatewayInfo reads the gateway auth token and port from the OpenClaw config.
func (c *Openclaw) gatewayInfo() (token string, port int) {
	port = defaultGatewayPort
	home, err := os.UserHomeDir()
	if err != nil {
		return "", port
	}

	for _, path := range []string{
		filepath.Join(home, ".openclaw", "openclaw.json"),
		filepath.Join(home, ".clawdbot", "clawdbot.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config map[string]any
		if json.Unmarshal(data, &config) != nil {
			continue
		}
		gw, _ := config["gateway"].(map[string]any)
		if p, ok := gw["port"].(float64); ok && p > 0 {
			port = int(p)
		}
		auth, _ := gw["auth"].(map[string]any)
		if t, _ := auth["token"].(string); t != "" {
			token = t
		}
		return token, port
	}
	return "", port
}

func printOpenclawReady(bin, token string, port int) {
	u := fmt.Sprintf("http://localhost:%d", port)
	if token != "" {
		u += "/#token=" + url.QueryEscape(token)
	}

	fmt.Fprintf(os.Stderr, "\n%s✓ OpenClaw is running%s\n\n", ansiGreen, ansiReset)
	fmt.Fprintf(os.Stderr, "  Open the Web UI:\n")
	fmt.Fprintf(os.Stderr, "    %s\n\n", hyperlink(u, u))
	fmt.Fprintf(os.Stderr, "  Or chat in the terminal:\n")
	fmt.Fprintf(os.Stderr, "    %s tui\n\n", bin)
	fmt.Fprintf(os.Stderr, "%s  Tip: connect WhatsApp, Telegram, and more with: %s configure --section channels%s\n", ansiGray, bin, ansiReset)
}

// portOpen checks if a TCP port is currently accepting connections.
func portOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func waitForPort(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func windowsHint(err error) error {
	if runtime.GOOS != "windows" {
		return err
	}
	return fmt.Errorf("%w\n\n"+
		"OpenClaw runs best on WSL2.\n"+
		"Quick setup: wsl --install\n"+
		"Guide: https://docs.openclaw.ai/windows", err)
}

// onboarded checks if OpenClaw onboarding wizard was completed
// by looking for the wizard.lastRunAt marker in the config
func (c *Openclaw) onboarded() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	configPath := filepath.Join(home, ".openclaw", "openclaw.json")
	legacyPath := filepath.Join(home, ".clawdbot", "clawdbot.json")

	config := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil {
		_ = json.Unmarshal(data, &config)
	} else if data, err := os.ReadFile(legacyPath); err == nil {
		_ = json.Unmarshal(data, &config)
	} else {
		return false
	}

	// Check for wizard.lastRunAt marker (set when onboarding completes)
	wizard, _ := config["wizard"].(map[string]any)
	if wizard == nil {
		return false
	}
	lastRunAt, _ := wizard["lastRunAt"].(string)
	return lastRunAt != ""
}

// hasBootstrap checks if the BOOTSTRAP.md file exists in the workspace,
// indicating this is the first launch and the intro ritual should trigger.
func (c *Openclaw) hasBootstrap() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, dir := range []string{".openclaw", ".clawdbot"} {
		if _, err := os.Stat(filepath.Join(home, dir, "workspace", "BOOTSTRAP.md")); err == nil {
			return true
		}
	}
	return false
}

func ensureOpenclawInstalled() (string, error) {
	if _, err := exec.LookPath("openclaw"); err == nil {
		return "openclaw", nil
	}
	if _, err := exec.LookPath("clawdbot"); err == nil {
		return "clawdbot", nil
	}

	if _, err := exec.LookPath("npm"); err != nil {
		return "", fmt.Errorf("openclaw is not installed and npm was not found\n\n" +
			"To install OpenClaw, first install Node.js (>= 22.12.0):\n" +
			"  https://nodejs.org/\n\n" +
			"Then run:\n" +
			"  npm install -g openclaw@latest")
	}

	if err := checkNodeVersion(); err != nil {
		return "", err
	}

	ok, err := confirmPrompt("OpenClaw is not installed. Install with npm?")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("openclaw installation cancelled")
	}

	fmt.Fprintf(os.Stderr, "\nInstalling OpenClaw...\n")
	cmd := exec.Command("npm", "install", "-g", "openclaw@latest")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if os.IsPermission(err) {
			return "", fmt.Errorf("permission denied installing openclaw\n\nTry running:\n  sudo npm install -g openclaw@latest")
		}
		return "", fmt.Errorf("failed to install openclaw: %w", err)
	}

	if _, err := exec.LookPath("openclaw"); err != nil {
		return "", fmt.Errorf("openclaw was installed but the binary was not found on PATH\n\nYou may need to restart your shell")
	}

	fmt.Fprintf(os.Stderr, "%sOpenClaw installed successfully%s\n\n", ansiGreen, ansiReset)
	return "openclaw", nil
}

func checkNodeVersion() error {
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		return fmt.Errorf("openclaw requires Node.js (>= 22.12.0) but node was not found\n\n" +
			"Install from: https://nodejs.org/")
	}

	version := strings.TrimSpace(string(out))
	if !semver.IsValid(version) {
		return fmt.Errorf("unexpected node version format: %s", version)
	}

	if semver.Compare(version, "v22.12.0") < 0 {
		return fmt.Errorf("openclaw requires Node.js >= 22.12.0 but found %s\n\n"+
			"Update from: https://nodejs.org/", version)
	}
	return nil
}

func (c *Openclaw) Paths() []string {
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".openclaw", "openclaw.json")
	if _, err := os.Stat(p); err == nil {
		return []string{p}
	}
	legacy := filepath.Join(home, ".clawdbot", "clawdbot.json")
	if _, err := os.Stat(legacy); err == nil {
		return []string{legacy}
	}
	return nil
}

func (c *Openclaw) Edit(models []string) error {
	if len(models) == 0 {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	configPath := filepath.Join(home, ".openclaw", "openclaw.json")
	legacyPath := filepath.Join(home, ".clawdbot", "clawdbot.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	// Read into map[string]any to preserve unknown fields
	config := make(map[string]any)
	if data, err := os.ReadFile(configPath); err == nil {
		_ = json.Unmarshal(data, &config)
	} else if data, err := os.ReadFile(legacyPath); err == nil {
		_ = json.Unmarshal(data, &config)
	}

	// Navigate/create: models.providers.ollama (preserving other providers)
	modelsSection, _ := config["models"].(map[string]any)
	if modelsSection == nil {
		modelsSection = make(map[string]any)
	}
	providers, _ := modelsSection["providers"].(map[string]any)
	if providers == nil {
		providers = make(map[string]any)
	}
	ollama, _ := providers["ollama"].(map[string]any)
	if ollama == nil {
		ollama = make(map[string]any)
	}

	ollama["baseUrl"] = envconfig.Host().String() + "/v1"
	// needed to register provider
	ollama["apiKey"] = "ollama-local"
	// TODO(parthsareen): potentially move to responses
	ollama["api"] = "openai-completions"

	// Build map of existing models to preserve user customizations
	existingModels, _ := ollama["models"].([]any)
	existingByID := make(map[string]map[string]any)
	for _, m := range existingModels {
		if entry, ok := m.(map[string]any); ok {
			if id, ok := entry["id"].(string); ok {
				existingByID[id] = entry
			}
		}
	}

	client, _ := api.ClientFromEnvironment()

	var newModels []any
	for _, m := range models {
		entry := openclawModelConfig(context.Background(), client, m)
		// Merge existing fields (user customizations)
		if existing, ok := existingByID[m]; ok {
			for k, v := range existing {
				if _, isNew := entry[k]; !isNew {
					entry[k] = v
				}
			}
		}
		newModels = append(newModels, entry)
	}
	ollama["models"] = newModels

	providers["ollama"] = ollama
	modelsSection["providers"] = providers
	config["models"] = modelsSection

	// Update agents.defaults.model.primary (preserving other agent settings)
	agents, _ := config["agents"].(map[string]any)
	if agents == nil {
		agents = make(map[string]any)
	}
	defaults, _ := agents["defaults"].(map[string]any)
	if defaults == nil {
		defaults = make(map[string]any)
	}
	modelConfig, _ := defaults["model"].(map[string]any)
	if modelConfig == nil {
		modelConfig = make(map[string]any)
	}
	modelConfig["primary"] = "ollama/" + models[0]
	defaults["model"] = modelConfig
	agents["defaults"] = defaults
	config["agents"] = agents

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return writeWithBackup(configPath, data)
}

// openclawModelConfig builds an OpenClaw model config entry with capability detection.
func openclawModelConfig(ctx context.Context, client *api.Client, modelID string) map[string]any {
	entry := map[string]any{
		"id":    modelID,
		"name":  modelID,
		"input": []any{"text"},
		"cost": map[string]any{
			"input":      0,
			"output":     0,
			"cacheRead":  0,
			"cacheWrite": 0,
		},
	}

	// Cloud models: use hardcoded limits
	if isCloudModel(ctx, client, modelID) {
		if l, ok := lookupCloudModelLimit(modelID); ok {
			entry["contextWindow"] = l.Context
			entry["maxTokens"] = l.Output
		}
		return entry
	}

	if client == nil {
		return entry
	}

	resp, err := client.Show(ctx, &api.ShowRequest{Model: modelID})
	if err != nil {
		return entry
	}

	// Set input types based on vision capability
	if slices.Contains(resp.Capabilities, model.CapabilityVision) {
		entry["input"] = []any{"text", "image"}
	}

	// Set reasoning based on thinking capability
	if slices.Contains(resp.Capabilities, model.CapabilityThinking) {
		entry["reasoning"] = true
	}

	// Extract context window from ModelInfo
	for key, val := range resp.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			if ctxLen, ok := val.(float64); ok && ctxLen > 0 {
				entry["contextWindow"] = int(ctxLen)
			}
			break
		}
	}

	return entry
}

func (c *Openclaw) Models() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	config, err := readJSONFile(filepath.Join(home, ".openclaw", "openclaw.json"))
	if err != nil {
		config, err = readJSONFile(filepath.Join(home, ".clawdbot", "clawdbot.json"))
		if err != nil {
			return nil
		}
	}

	modelsSection, _ := config["models"].(map[string]any)
	providers, _ := modelsSection["providers"].(map[string]any)
	ollama, _ := providers["ollama"].(map[string]any)
	modelList, _ := ollama["models"].([]any)

	var result []string
	for _, m := range modelList {
		if entry, ok := m.(map[string]any); ok {
			if id, ok := entry["id"].(string); ok {
				result = append(result, id)
			}
		}
	}
	return result
}
