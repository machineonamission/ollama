package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ollama/ollama/envconfig"
	"golang.org/x/mod/semver"
)

const defaultGatewayPort = 18789

type Openclaw struct{}

func (c *Openclaw) String() string { return "OpenClaw" }

func (c *Openclaw) Run(model string, args []string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("OpenClaw runs best on WSL2\n\n" +
			"Quick setup:\n" +
			"  wsl --install\n\n" +
			"Then run ollama launch openclaw from inside WSL.\n" +
			"Guide: https://docs.openclaw.ai/windows")
	}

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
			"--skip-channels",
			"--skip-skills",
		)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("openclaw onboarding failed: %w\n\nTry running: openclaw onboard", err)
		}
	}

	// Run gateway in the foreground so the user can see it's alive
	// and ctrl+C cleanly shuts it down.
	token, port := c.gatewayInfo()
	printOpenclawReady(bin, token, port)
	fmt.Fprintf(os.Stderr, "%sPress Ctrl+C to stop%s\n\n", ansiGray, ansiReset)

	cmd := exec.Command(bin, "gateway", "run")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

	fmt.Fprintf(os.Stderr, "\nInstalling openclaw...\n")
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

	var newModels []any
	for _, model := range models {
		entry := map[string]any{
			"id":        model,
			"name":      model,
			"reasoning": false,
			"input":     []any{"text"},
			"cost": map[string]any{
				"input":      0,
				"output":     0,
				"cacheRead":  0,
				"cacheWrite": 0,
			},
			// TODO(parthsareen): get these values from API
			"contextWindow": 131072,
			"maxTokens":     16384,
		}
		// Merge existing fields (user customizations)
		if existing, ok := existingByID[model]; ok {
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
