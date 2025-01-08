package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxRetries        = 5
	shutdownTimeout   = 30 * time.Second
	validTokenPattern = `^IG[A-Za-z0-9_ZA-]+$`
	defaultConfigPath = "config.json"
)

type InstagramAccount struct {
	Token string `json:"token"`
}

type Config struct {
	Accounts    map[string]InstagramAccount `json:"-"`
	RefreshFreq string                      `json:"refresh_freq"`
	Port        string                      `json:"port"`
}

type TokenResponse struct {
	NewToken   string `json:"access_token"`
	TokenType  string `json:"token_type"`
	Permission string `json:"permissions"`
	ExpiresIn  int    `json:"expires_in"`
}

type TokenManager struct {
	accounts   map[string]*AccountHandler
	config     *Config
	server     *http.Server
	configPath string
	mutex      sync.RWMutex
}

type AccountHandler struct {
	lastRefresh  time.Time
	refreshTimer *time.Timer
	accountID    string
	token        string
	retryCount   int
	mutex        sync.RWMutex
}

func getDuration(freq string) time.Duration {
	switch freq {
	case "test":
		return 1 * time.Minute
	case "daily":
		return 24 * time.Hour
	case "weekly":
		return 7 * 24 * time.Hour
	case "monthly":
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func validateConfig(config *Config) error {
	// Validate port
	port, err := strconv.Atoi(config.Port)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port number: %s", config.Port)
	}

	// Validate tokens
	tokenPattern := regexp.MustCompile(validTokenPattern)
	for accountID, account := range config.Accounts {
		if !tokenPattern.MatchString(account.Token) {
			return fmt.Errorf("invalid token format for account %s", accountID)
		}
	}

	return nil
}

func (tm *TokenManager) refreshTokenWithRetry(ctx context.Context, accountID string, handler *AccountHandler) error {
	var err error
	backoff := time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			err = tm.refreshToken(accountID, handler)
			if err == nil {
				handler.mutex.Lock()
				handler.retryCount = 0
				handler.mutex.Unlock()
				return nil
			}

			handler.mutex.Lock()
			handler.retryCount++
			retryCount := handler.retryCount
			handler.mutex.Unlock()

			if retryCount >= maxRetries {
				log.Printf("Maximum retry attempts reached for account %s: %v", accountID, err)
				return fmt.Errorf("max retries exceeded: %v", err)
			}

			// Exponential backoff
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	return err
}

func (tm *TokenManager) refreshToken(accountID string, handler *AccountHandler) error {
	handler.mutex.RLock()
	currentToken := handler.token
	handler.mutex.RUnlock()

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get("https://graph.instagram.com/refresh_access_token?grant_type=ig_refresh_token&access_token=" + currentToken)
	if err != nil {
		return fmt.Errorf("error making request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-200 status code: %d", resp.StatusCode)
	} else {
		log.Printf("Token successfully refreshed for account: %s", accountID)
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("error parsing response: %v", err)
	}

	handler.mutex.Lock()
	handler.token = tokenResp.NewToken
	handler.lastRefresh = time.Now()
	handler.mutex.Unlock()

	// Save updated tokens to config
	if err := saveConfig(tm.config, tm); err != nil {
		log.Printf("Warning: Failed to save updated token to config: %v", err)
	}

	return nil
}

func (tm *TokenManager) scheduleNextRefresh(accountID string, handler *AccountHandler) {
	handler.mutex.Lock()
	defer handler.mutex.Unlock()

	if handler.refreshTimer != nil {
		handler.refreshTimer.Stop()
	}

	refreshInterval := getDuration(tm.config.RefreshFreq)
	handler.refreshTimer = time.AfterFunc(refreshInterval, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		if err := tm.refreshTokenWithRetry(ctx, accountID, handler); err != nil {
			log.Printf("Error refreshing token for %s: %v", accountID, err)
			// Reschedule with exponential backoff
			time.Sleep(time.Duration(handler.retryCount+1) * time.Minute)
			tm.scheduleNextRefresh(accountID, handler)
		} else {
			tm.scheduleNextRefresh(accountID, handler)
		}
	})
}

func (tm *TokenManager) Shutdown(ctx context.Context) error {
	// Stop all refresh timers
	tm.mutex.RLock()
	for _, handler := range tm.accounts {
		handler.mutex.Lock()
		if handler.refreshTimer != nil {
			handler.refreshTimer.Stop()
		}
		handler.mutex.Unlock()
	}
	tm.mutex.RUnlock()

	// Save final state
	if err := saveConfig(tm.config, tm); err != nil {
		log.Printf("Error saving config during shutdown: %v", err)
	}

	// Shutdown HTTP server
	return tm.server.Shutdown(ctx)
}

func (tm *TokenManager) setupRefreshes() {
	tm.mutex.RLock()
	defer tm.mutex.RUnlock()

	for accountID, handler := range tm.accounts {
		handler.mutex.Lock()
		handler.lastRefresh = time.Now() // Initialize with current time
		handler.mutex.Unlock()

		tm.scheduleNextRefresh(accountID, handler)
	}

	log.Printf("Individual refresh timers set up for all accounts")
}

func loadConfig(configPath string) (*Config, error) {
	file, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("error reading config file: %v", err)
	}

	var rawConfig map[string]json.RawMessage
	if err := json.Unmarshal(file, &rawConfig); err != nil {
		return nil, fmt.Errorf("error parsing config: %v", err)
	}

	config := &Config{
		Accounts: make(map[string]InstagramAccount),
	}

	// Extract refresh_freq and port
	if freq, ok := rawConfig["refresh_freq"]; ok {
		json.Unmarshal(freq, &config.RefreshFreq)
	}
	if port, ok := rawConfig["port"]; ok {
		json.Unmarshal(port, &config.Port)
	}

	// Extract Instagram accounts
	for key, value := range rawConfig {
		if key != "refresh_freq" && key != "port" {
			var account InstagramAccount
			if err := json.Unmarshal(value, &account); err != nil {
				return nil, fmt.Errorf("error parsing account %s: %v", key, err)
			}
			config.Accounts[key] = account
		}
	}

	// Validate refresh frequency
	switch config.RefreshFreq {
	case "daily", "weekly", "monthly", "test":
		// Valid frequency
	default:
		return nil, fmt.Errorf("invalid refresh frequency: %s", config.RefreshFreq)
	}

	return config, nil
}

func saveConfig(config *Config, tokenManager *TokenManager) error {
	// Create the output structure
	output := make(map[string]interface{})
	output["refresh_freq"] = config.RefreshFreq
	output["port"] = config.Port

	// Add all account tokens
	tokenManager.mutex.RLock()
	for id, handler := range tokenManager.accounts {
		handler.mutex.RLock()
		output[id] = InstagramAccount{Token: handler.token}
		handler.mutex.RUnlock()
	}
	tokenManager.mutex.RUnlock()

	// Save to file
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling config: %v", err)
	}

	return os.WriteFile(tokenManager.configPath, data, 0o644)
}

func enableCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

func (tm *TokenManager) handleGetToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract account ID from path
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 3 || parts[1] != "token" {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	accountID := parts[2]

	tm.mutex.RLock()
	handler, exists := tm.accounts[accountID]
	tm.mutex.RUnlock()

	if !exists {
		http.Error(w, "Account not found", http.StatusNotFound)
		return
	}

	handler.mutex.RLock()
	response := map[string]string{"token": handler.token}
	handler.mutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (tm *TokenManager) manualRefresh(accountID string) error {
	tm.mutex.RLock()
	handler, exists := tm.accounts[accountID]
	tm.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("account %s not found", accountID)
	}

	return tm.refreshToken(accountID, handler)
}

func main() {
	var configPath string

	refreshCmd := flag.NewFlagSet("refresh", flag.ExitOnError)
	refreshConfigPath := refreshCmd.String("config", defaultConfigPath, "path to config file")

	// Main flags
	flag.StringVar(&configPath, "config", defaultConfigPath, "path to config file")
	flag.Parse()

	// Parse initial arguments
	if len(os.Args) > 1 && os.Args[1] == "refresh" {
		refreshCmd.Parse(os.Args[2:])

		if refreshCmd.NArg() != 1 {
			log.Fatal("Usage: instatokend refresh [-config path/to/config.json] <account_id>")
		}

		accountID := refreshCmd.Arg(0)

		// Load configuration
		config, err := loadConfig(*refreshConfigPath)
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}

		// Initialize token manager
		tokenManager := &TokenManager{
			accounts:   make(map[string]*AccountHandler),
			config:     config,
			configPath: *refreshConfigPath,
		}

		// Initialize account handlers
		for id, account := range config.Accounts {
			tokenManager.accounts[id] = &AccountHandler{
				accountID: id,
				token:     account.Token,
			}
		}

		// Perform manual refresh
		if err := tokenManager.manualRefresh(accountID); err != nil {
			log.Fatalf("Error refreshing token for %s: %v", accountID, err)
		}

		log.Printf("Successfully refreshed token for account: %s", accountID)
		return
	}

	// Load configuration
	config, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	if err := validateConfig(config); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	tokenManager := &TokenManager{
		accounts:   make(map[string]*AccountHandler),
		config:     config,
		configPath: configPath,
	}

	// Initialize account handlers
	for id, account := range config.Accounts {
		tokenManager.accounts[id] = &AccountHandler{
			accountID: id,
			token:     account.Token,
		}
	}

	// Set up HTTP server with timeouts
	tokenManager.server = &http.Server{
		Addr:         ":" + config.Port,
		Handler:      http.HandlerFunc(enableCORS(tokenManager.handleGetToken)),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Set up individual refresh timers
	tokenManager.setupRefreshes()

	// Graceful shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Initiating graceful shutdown...")

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := tokenManager.Shutdown(ctx); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}
		os.Exit(0)
	}()

	log.Printf("Starting server on port %s", config.Port)
	if err := tokenManager.server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Error starting server: %v", err)
	}
}
