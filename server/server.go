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
		time.Now().UTC().Format(time.RFC3339),
		"SYSTEM",
		"SYSTEM",
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
				time.Now().UTC().Format(time.RFC3339),
				"SYSTEM",
				"SYSTEM",
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
				time.Now().UTC().Format(time.RFC3339),
				"SYSTEM",
				"SYSTEM",
				"Server shut down",
			)
			fmt.Println("\nServer shut down...")
			return
		}
	}
}

func handleConnection(conn net.Conn, rl *RateLimiter, cfg *Config) {
	defer conn.Close()

	// Get client ID (simple example)
	clientID := conn.RemoteAddr().String()

	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
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

// Configuration System
type Config struct {
	Port           int      `yaml:"port"`
	MaxConnections int      `yaml:"max_connections"`
	LogFormat      string   `yaml:"log_format"`
	RateLimit      int      `yaml:"rate_limit"`
	LogPath        string   `yaml:"log_path"`       // Where to store logs
	LogMaxSize     int64    `yaml:"log_max_size"`   // Max file size before rotation
	ClientTimeout  int      `yaml:"client_timeout"` // In seconds
	AllowedLevels  []string `yaml:"allowed_levels"`
}

func loadConfig() Config {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		panic(fmt.Errorf("config error: %v", err))
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		panic(fmt.Errorf("invalid config: %v", err))
	}
	return cfg
}

// Rate limiter structure, token bucket per client
type RateLimiter struct {
	tokens    map[string]int
	lastReset time.Time
	mu        sync.Mutex
	cfg       *Config
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

// Updated Log Processing Component
func processLog(conn net.Conn, clientID string, msg LogMessage, rl *RateLimiter, cfg *Config) {
	// 1. Format validation
	if !isValidMessage(msg, cfg) {
		fmt.Printf("Invalid message format from %s\n", clientID)
		return
	}

	// 2. Timestamp standardization
	timestamp := time.Unix(int64(msg.Timestamp), 0).UTC().Format(time.RFC3339)

	// 3. log using cfg format
	logEntry := fmt.Sprintf(cfg.LogFormat, timestamp, msg.Level, clientID, msg.Message)

	// 4. Rate limiting (token bucket per client)
	if !rl.Allow(clientID) {
		conn.Write([]byte("RATE_LIMITED"))
		fmt.Printf("Rate limit exceeded for %s\n", clientID)
		// record in log
		rateLimitedEntry := fmt.Sprintf(cfg.LogFormat,
			time.Now().UTC().Format(time.RFC3339),
			"SYSTEM",
			clientID,
			"RATE_LIMITED: "+msg.Message,
		)
		logQueue <- rateLimitedEntry
		return
	}

	// 5. Message queuing (channel buffer)
	logQueue <- logEntry
}

// Helper functions
var (
	logQueue = make(chan string, 1000)
)

func isValidMessage(msg LogMessage, cfg *Config) bool {
	for _, level := range cfg.AllowedLevels {
		if msg.Level == level {
			return true
		}
	}
	return false
}

// File I/O Component
type LogWriter struct {
	currentFile *os.File
	filePath    string
	maxSize     int64
	mu          sync.Mutex
}

func NewLogWriter(path string, maxSize int64) *LogWriter {
	return &LogWriter{
		filePath: path,
		maxSize:  maxSize, // Use provided size
	}
}

func (lw *LogWriter) Write(entry string) error {
	lw.mu.Lock()
	defer lw.mu.Unlock()

	// Create file if needed
	if lw.currentFile == nil {
		if err := lw.rotate(); err != nil {
			return err
		}
	}

	// Check size for rotation
	if info, _ := lw.currentFile.Stat(); info.Size() > lw.maxSize {
		lw.rotate()
	}

	_, err := fmt.Fprintln(lw.currentFile, entry)
	return err
}

func (lw *LogWriter) rotate() error {
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
			backupPath := fmt.Sprintf("%s%d%s", base, backupNum, ext)
			if _, err := os.Stat(backupPath); os.IsNotExist(err) {
				if err := os.Rename(lw.filePath, backupPath); err != nil {
					return fmt.Errorf("failed to rotate log file: %v", err)
				}
				break
			}
			backupNum++
		}
	}

	// Create new file
	newFile, err := os.OpenFile(lw.filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to create new log file: %v", err)
	}
	lw.currentFile = newFile
	return nil
}

type LogMessage struct {
	Level     string  `json:"level"`
	Message   string  `json:"message"`
	Timestamp float64 `json:"timestamp"`
	Source    string  `json:"source"`
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
			if err := writer.Write(entry); err != nil {
				fmt.Printf("Failed to write log: %v\n", err)
			}
		}
	}()

	// Implement dynamic config reload
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			cfg = loadConfig()
		}
	}()

	startServer(cfg, rateLimiter)
}
