package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// Default buffer size for reading messages
	READ_BUFFER_SIZE = 1024
)

// Configuration System
type Config struct {
	SystemName             string         `yaml:"system_name"` // Name of the system
	Port                   int            `yaml:"port"`
	MaxConnections         int            `yaml:"max_connections"`
	LogFormat              string         `yaml:"log_format"`
	RateLimit              int            `yaml:"rate_limit"`
	RateLimitedLogInterval time.Duration  `yaml:"rate_limited_log_interval"`
	LogPath                string         `yaml:"log_path"`       // Where to store logs
	LogMaxSize             int64          `yaml:"log_max_size"`   // Max file size before rotation
	ClientTimeout          int            `yaml:"client_timeout"` // In seconds
	LogLevels              map[string]int `yaml:"log_levels"`     // log level and its priority
	MinLogLevel            int            `yaml:"min_log_level"`  // Minimum log level to log
}

// File I/O Component
type LogWriter struct {
	currentFile *os.File
	filePath    string
	maxSize     int64
	mu          sync.Mutex
}

type LogMessage struct {
	Level     string  `json:"level"`
	Message   string  `json:"message"`
	Timestamp float64 `json:"timestamp"`
	Source    string  `json:"source"`
}

// Rate limiter structure, token bucket per client
type RateLimiter struct {
	tokens    map[string]int
	lastReset time.Time
	mu        sync.Mutex
	cfg       *Config
}

var (
	logQueue              = make(chan string, 1000)
	rateLimitedLogTimesMu sync.Mutex
	rateLimitedLogTimes   = make(map[string]time.Time)
)

// Network Listener Component
func startServer(cfg Config, rl *RateLimiter) {
	// 1. TCP Port Binding
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("Server listening on port %d\n", cfg.Port)

	// log server listening message
	logQueue <- fmt.Sprintf(cfg.LogFormat,
		time.Now().Format(time.RFC3339),
		"INFO",
		cfg.SystemName,
		fmt.Sprintf("Server started on port %d", cfg.Port),
	)
	// 2. Concurrent Connection Handling
	connChan := make(chan net.Conn)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				fmt.Println("Error accepting connection:", err)
				continue
			}
			connChan <- conn

			// log new connection message
			logQueue <- fmt.Sprintf(cfg.LogFormat,
				time.Now().Format(time.RFC3339),
				"INFO",
				cfg.SystemName,
				fmt.Sprintf("New connection from %s", conn.RemoteAddr()),
			)

		}
	}()

	// Server shutdown handling
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case conn := <-connChan:
			go handleConnection(conn, rl, &cfg)
		case <-stop:
			// log server shut down message
			logQueue <- fmt.Sprintf(cfg.LogFormat,
				time.Now().Format(time.RFC3339),
				"INFO",
				cfg.SystemName,
				"Server shut down",
			)
			fmt.Println("\nServer shut down...")
			return
		}
	}
}

func handleConnection(conn net.Conn, rl *RateLimiter, cfg *Config) {
	defer conn.Close()

	// Set the waiting time before the client sends its first data
	if err := conn.SetDeadline(time.Now().Add(
		time.Duration(cfg.ClientTimeout) * time.Second)); err != nil {
		fmt.Printf("%s Initial timeout setting failed: %v\n",
			conn.RemoteAddr().String(), err)
		logQueue <- fmt.Sprintf(cfg.LogFormat,
			time.Now().Format(time.RFC3339),
			"ERROR",
			cfg.SystemName,
			fmt.Sprintf("%s Initial timeout setting failed: %v",
				conn.RemoteAddr().String(), err),
		)
		return
	}
	// Get client ID (simple example)
	clientID := conn.RemoteAddr().String()

	buf := make([]byte, READ_BUFFER_SIZE)
	for {
		// Set the waiting time before the client sends its first data
		if err := conn.SetDeadline(time.Now().Add(
			time.Duration(cfg.ClientTimeout) * time.Second)); err != nil {
			fmt.Printf("%s Reset timeout failed: %v\n",
				conn.RemoteAddr().String(), err)

			// log client reset timeout message
			logQueue <- fmt.Sprintf(cfg.LogFormat,
				time.Now().Format(time.RFC3339),
				"ERROR",
				cfg.SystemName,
				fmt.Sprintf("%s Reset timeout failed: %v",
					conn.RemoteAddr().String(), err),
			)
			break
		}
		n, err := conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				fmt.Printf("%s Disconnected after timeout: %v\n",
					conn.RemoteAddr().String(), err)

				// log client disconnect message
				logQueue <- fmt.Sprintf(cfg.LogFormat,
					time.Now().Format(time.RFC3339),
					"INFO",
					cfg.SystemName,
					fmt.Sprintf("%s Disconnected after timeout: %v",
						conn.RemoteAddr().String(), err),
				)
			} else {
				fmt.Printf("%s Read error: %v", clientID, err)
				// log client read error message
				logQueue <- fmt.Sprintf(cfg.LogFormat,
					time.Now().Format(time.RFC3339),
					"ERROR",
					cfg.SystemName,
					fmt.Sprintf("%s Read error: %v",
						conn.RemoteAddr().String(), err),
				)
			}
			break
		}
		var msg LogMessage
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			conn.Write([]byte("INVALID_JSON"))
			continue
		}

		// Process log with validation and rate limiting
		processLog(conn, clientID, msg, rl, cfg)

		conn.Write([]byte("ACK: " + msg.Message))
	}
}

func loadConfig() Config {
	data, err := os.ReadFile("../config.yaml")
	if err != nil {
		panic(fmt.Errorf("config error: %v", err))
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		panic(fmt.Errorf("invalid config: %v", err))
	}
	return cfg
}

func NewRateLimiter(cfg *Config) *RateLimiter {
	return &RateLimiter{
		tokens:    make(map[string]int),
		lastReset: time.Now(),
		cfg:       cfg,
	}
}

// Rate limiter methods
func (rl *RateLimiter) Allow(clientID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Initialize client if not exists
	if _, exists := rl.tokens[clientID]; !exists {
		rl.tokens[clientID] = rl.cfg.RateLimit
	}

	// Reset tokens every Second
	if time.Since(rl.lastReset) > time.Second {
		for k := range rl.tokens {
			rl.tokens[k] = rl.cfg.RateLimit
		}
		rl.lastReset = time.Now()
	}

	if rl.tokens[clientID] <= 0 {
		return false
	}

	rl.tokens[clientID]--
	return true
}

// Log Processing Component
func processLog(conn net.Conn, clientID string, msg LogMessage,
	rl *RateLimiter, cfg *Config) {
	// 1. Format validation
	if !isValidMessage(msg, cfg) {
		fmt.Printf("Invalid message format from %s\n", clientID)
		return
	}

	// 2. Timestamp standardization
	timestamp := time.Unix(int64(msg.Timestamp), 0).Format(time.RFC3339)

	// 3. log using cfg format
	logEntry := fmt.Sprintf(cfg.LogFormat,
		timestamp, msg.Level, clientID, msg.Message)

	// 4. Rate limiting (token bucket per client)
	if !rl.Allow(clientID) {
		conn.Write([]byte("RATE_LIMITED"))
		fmt.Printf("Rate limit exceeded for %s\n", clientID)

		// RATE_LIMITED entry needs to be limited as well
		now := time.Now()
		lastLogTime, exists := rateLimitedLogTimes[clientID]
		if !exists || now.Sub(lastLogTime) > cfg.RateLimitedLogInterval {
			// Log RATE_LIMITED and update last log time
			rateLimitedEntry := fmt.Sprintf(cfg.LogFormat,
				time.Now().Format(time.RFC3339),
				"WARN",
				cfg.SystemName,
				"RATE_LIMITED: "+clientID,
			)
			logQueue <- rateLimitedEntry
			rateLimitedLogTimesMu.Lock()
			defer rateLimitedLogTimesMu.Unlock()
			rateLimitedLogTimes[clientID] = now
		}
		return
	}

	// 5. Message queuing (channel buffer)
	logQueue <- logEntry
}

// Helper functions

// Message validation based on config
// TODO: Add more validation rules
func isValidMessage(msg LogMessage, cfg *Config) bool {

	// Check if log level is valid
	levelPriority, exists := cfg.LogLevels[msg.Level]
	if !exists {
		return false
	}
	// check if log level is above min log level
	return levelPriority >= cfg.MinLogLevel
}

func NewLogWriter(path string, maxSize int64) *LogWriter {
	return &LogWriter{
		filePath: path,
		maxSize:  maxSize, // Use provided size
	}
}

func (lw *LogWriter) Write(entry string, cfg *Config) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	// Create file if needed
	if lw.currentFile == nil {
		if err := lw.rotate(cfg); err != nil {
			return err
		}
	}

	// Check size for rotation
	if info, _ := lw.currentFile.Stat(); info.Size() > lw.maxSize {
		lw.rotate(cfg)
	}

	_, err := fmt.Fprintln(lw.currentFile, entry)
	return err
}

func (lw *LogWriter) rotate(cfg *Config) error {
	// Close current file if open
	if lw.currentFile != nil {
		lw.currentFile.Close()
	}

	// Check if file exists
	if _, err := os.Stat(lw.filePath); err == nil {
		// Find next available backup number
		var backupNum int
		ext := filepath.Ext(lw.filePath)             // Get extension (.log)
		base := strings.TrimSuffix(lw.filePath, ext) // Get filename without extension

		for {
			// New format: base + number + extension (app0.log, app1.log)
			backupPath := fmt.Sprintf("%s_%s%s",
				base, time.Now().Format("20060102_150405"), ext)
			if _, err := os.Stat(backupPath); os.IsNotExist(err) {
				if err := os.Rename(lw.filePath, backupPath); err != nil {

					logQueue <- fmt.Sprintf(cfg.LogFormat,
						time.Now().Format(time.RFC3339),
						"ERROR",
						cfg.SystemName,
						fmt.Sprintf("Failed to rotate log file: %v", err),
					)
					return fmt.Errorf("failed to rotate log file: %v", err)
				}
				break
			}
			backupNum++
		}
	}

	// Create new file
	newFile, err := os.OpenFile(lw.filePath,
		os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		// Handle error
		logQueue <- fmt.Sprintf(cfg.LogFormat,
			time.Now().Format(time.RFC3339),
			"ERROR",
			cfg.SystemName,
			fmt.Sprintf("Failed to create new log file: %v", err),
		)
		return fmt.Errorf("failed to create new log file: %v", err)
	}
	lw.currentFile = newFile
	return nil
}

func main() {
	cfg := loadConfig()

	// Initialize rate limiter with config
	rateLimiter := NewRateLimiter(&cfg)

	// Initialize log writer
	writer := NewLogWriter(cfg.LogPath, cfg.LogMaxSize)

	// Start log consumer
	go func() {
		for entry := range logQueue {
			if err := writer.Write(entry, &cfg); err != nil {
				fmt.Printf("Failed to write log: %v\n", err)
			}
		}
	}()

	// Implement dynamic config reload
	// not working on Windows
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			cfg = loadConfig()
		}
	}()

	startServer(cfg, rateLimiter)
}
