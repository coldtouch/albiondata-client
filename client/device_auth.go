package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ao-data/albiondata-client/log"
	"github.com/spf13/viper"
)

const vpsBaseURL = "https://albionaitool.xyz"

// HTTP client with timeout for device auth requests
var authHTTPClient = &http.Client{Timeout: 15 * time.Second}

type deviceCodeResponse struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type deviceTokenResponse struct {
	CaptureToken string `json:"capture_token"`
	Username     string `json:"username"`
	Error        string `json:"error"`
}

// RunDeviceAuth runs the OAuth Device Authorization flow
// Returns the capture token on success
func RunDeviceAuth() (string, error) {
	// Step 1: Request a device code
	resp, err := authHTTPClient.Post(vpsBaseURL+"/api/device/code", "application/json", nil)
	if err != nil {
		return "", fmt.Errorf("failed to request device code: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var codeResp deviceCodeResponse
	if err := json.Unmarshal(body, &codeResp); err != nil {
		return "", fmt.Errorf("failed to parse device code response: %v", err)
	}

	// Step 2: Show the user the code and URL
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║     COLDTOUCH DATA CLIENT - DEVICE LOGIN     ║")
	fmt.Println("╠══════════════════════════════════════════════╣")
	fmt.Println("║                                              ║")
	fmt.Printf("║  1. Open your browser and go to:             ║\n")
	fmt.Printf("║     %-40s ║\n", codeResp.VerificationURI)
	fmt.Println("║                                              ║")
	fmt.Printf("║  2. Enter code:  %-28s║\n", codeResp.UserCode)
	fmt.Println("║                                              ║")
	fmt.Println("║  3. Click 'Authorize' on the website         ║")
	fmt.Println("║                                              ║")
	fmt.Printf("║  Code expires in %d minutes                  ║\n", codeResp.ExpiresIn/60)
	fmt.Println("║                                              ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("Waiting for authorization...")

	// Step 3: Poll for authorization
	interval := time.Duration(codeResp.Interval) * time.Second
	if interval < 3*time.Second {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(codeResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)

		tokenReq := map[string]string{"device_code": codeResp.DeviceCode}
		tokenBody, _ := json.Marshal(tokenReq)

		resp, err := authHTTPClient.Post(vpsBaseURL+"/api/device/token", "application/json", bytes.NewReader(tokenBody))
		if err != nil {
			continue // Network error, retry
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var tokenResp deviceTokenResponse
		json.Unmarshal(respBody, &tokenResp)

		if resp.StatusCode == 428 {
			// authorization_pending — keep polling
			fmt.Print(".")
			continue
		}

		if resp.StatusCode == 200 && tokenResp.CaptureToken != "" {
			fmt.Printf("\n\n✓ Authorized as: %s\n", tokenResp.Username)
			fmt.Println("Token saved — you won't need to do this again.")
			return tokenResp.CaptureToken, nil
		}

		if tokenResp.Error == "expired_token" {
			return "", fmt.Errorf("device code expired — please try again")
		}
	}

	return "", fmt.Errorf("authorization timed out — please try again")
}

// EnsureCaptureToken checks for an existing token or runs device auth
func EnsureCaptureToken() string {
	// Check CLI flag first
	if ConfigGlobal.CaptureToken != "" {
		return ConfigGlobal.CaptureToken
	}

	// Check config file
	token := viper.GetString("CaptureToken")
	if token != "" {
		log.Infof("[Auth] Loaded capture token from config")
		return token
	}

	// No token found — run device auth
	log.Info("[Auth] No capture token found — starting device authorization...")
	fmt.Println("\nNo capture token found. Let's link this client to your account.")

	newToken, err := RunDeviceAuth()
	if err != nil {
		log.Errorf("[Auth] Device auth failed: %v", err)
		fmt.Printf("\nDevice auth failed: %v\n", err)
		fmt.Println("You can still use the client for AODP data — chest captures will be local only.")
		return ""
	}

	// Save to config file (resolve relative to executable path, not CWD)
	viper.Set("CaptureToken", newToken)
	exePath, _ := os.Executable()
	configDir := filepath.Dir(exePath)
	configFile := filepath.Join(configDir, "config.yaml")
	if err := writeConfigFile(configFile, newToken); err != nil {
		log.Warnf("[Auth] Could not save token to config: %v", err)
		fmt.Printf("Warning: Could not save token to %s — you'll need to authorize again next time.\n", configFile)
	} else {
		log.Infof("[Auth] Token saved to %s", configFile)
	}

	return newToken
}

// writeConfigFile merges the new CaptureToken into any existing config.yaml
// rather than overwriting it. Preserves user-set keys like LogUnknownEvents,
// VPSRelayURL, CaptureEnabled, etc. — previously this function truncated the
// file to just the token line, silently wiping discovery flags mid-session.
func writeConfigFile(path string, token string) error {
	existing, _ := os.ReadFile(path)
	var lines []string
	if len(existing) > 0 {
		for _, line := range strings.Split(strings.ReplaceAll(string(existing), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			// Skip any prior CaptureToken line — we'll rewrite it below.
			if strings.HasPrefix(trimmed, "CaptureToken:") || strings.HasPrefix(trimmed, "CaptureToken ") {
				continue
			}
			lines = append(lines, line)
		}
		// Remove trailing empty lines left by the filter above
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
	} else {
		lines = append(lines, "# Coldtouch Market Analyzer - Custom Data Client Config")
	}
	lines = append(lines, fmt.Sprintf("CaptureToken: \"%s\"", token))
	content := strings.Join(lines, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0600)
}
