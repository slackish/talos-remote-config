package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

var (
	// version is set via ldflags at build time
	version = "dev"

	dataDir       = flag.String("data-dir", "./data", "Directory containing metadata files")
	port          = flag.String("port", "8080", "Port to listen on")
	commonConfig  = flag.String("common-config", "", "Path to common configuration file (applied to every request)")
	defaultConfig = flag.String("default-config", "", "Path to default configuration file (returned for unmatched requests)")

	ipACLEnabled    = flag.Bool("ip-acl-enabled", false, "Enable IP-based access control")
	ipACLConfig     = flag.String("ip-acl-config", "", "Path to IP-to-metadata mapping YAML file")
	ipACLForwarded  = flag.String("ip-acl-forwarded", "", "HTTP header name to resolve client IP (e.g., X-Forwarded-For)")
)

var (
	ipACLMapping atomic.Pointer[map[string]map[string]string]
)

func clientIP(r *http.Request, headerName string) string {
	if headerName != "" {
		val := r.Header.Get(headerName)
		if val != "" {
			ip := strings.Split(val, ",")[0]
			return strings.TrimSpace(ip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func loadIPMapping(path string) (map[string]map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mapping file: %w", err)
	}
	var result map[string]map[string]string
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse mapping file: %w", err)
	}
	return result, nil
}

func startMappingWatcher(path string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatalf("Failed to create fsnotify watcher for %s: %v", path, err)
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove) != 0 {
					// Small delay to allow multiple rapid events to coalesce
					time.Sleep(50 * time.Millisecond)
					mapping, err := loadIPMapping(path)
					if err != nil {
						log.Printf("IP ACL: failed to reload mapping file %s: %v", path, err)
						ipACLMapping.Store(nil)
					} else {
						log.Printf("IP ACL: mapping file reloaded (%d entries)", len(mapping))
						ipACLMapping.Store(&mapping)
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("IP ACL: watcher error: %v", err)
			}
		}
	}()

	if err := watcher.Add(path); err != nil {
		log.Fatalf("Failed to watch mapping file %s: %v", path, err)
	}
}

func main() {
	flag.Parse()

	log.Printf("talos-remote-config version %s", version)

	// Set default paths for common and default configs if not specified
	if *commonConfig == "" {
		*commonConfig = filepath.Join(*dataDir, "common.yaml")
	}
	if *defaultConfig == "" {
		*defaultConfig = filepath.Join(*dataDir, "default.yaml")
	}

	// Load IP ACL mapping if enabled
	if *ipACLEnabled {
		if *ipACLConfig == "" {
			log.Fatal("IP ACL enabled but -ip-acl-config is not specified")
		}
		mapping, err := loadIPMapping(*ipACLConfig)
		if err != nil {
			log.Fatalf("Failed to load IP ACL mapping file %s: %v", *ipACLConfig, err)
		}
		ipACLMapping.Store(&mapping)
		log.Printf("IP ACL: loaded mapping file %s (%d entries)", *ipACLConfig, len(mapping))

		// Start hot-reload watcher
		startMappingWatcher(*ipACLConfig)
	}

	http.HandleFunc("/metadata", metadataHandler)
	http.HandleFunc("/", notFoundHandler)

	addr := fmt.Sprintf(":%s", *port)
	log.Printf("Starting server on %s, data directory: %s", addr, *dataDir)
	log.Printf("Common config: %s, Default config: %s", *commonConfig, *defaultConfig)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var paramLog []string
	for key, values := range query {
		for _, value := range values {
			paramLog = append(paramLog, fmt.Sprintf("%s=%s", key, value))
		}
	}

	log.Printf("Request: method=%s remote=%s path=%s params=[%s] user_agent=%q - 404 not found",
		r.Method, r.RemoteAddr, r.URL.Path, strings.Join(paramLog, ", "), r.Header.Get("User-Agent"))
	http.NotFound(w, r)
}

func metadataHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// Log all query parameters
	var paramLog []string
	for key, values := range query {
		for _, value := range values {
			paramLog = append(paramLog, fmt.Sprintf("%s=%s", key, value))
		}
	}

	// Get User-Agent and other useful headers
	userAgent := r.Header.Get("User-Agent")
	forwardedUser := r.Header.Get("X-Forwarded-User")
	forwardedFor := r.Header.Get("X-Forwarded-For")

	log.Printf("Request: method=%s remote=%s path=%s params=[%s] user_agent=%q forwarded_user=%q forwarded_for=%q",
		r.Method, r.RemoteAddr, r.URL.Path, strings.Join(paramLog, ", "), userAgent, forwardedUser, forwardedFor)

	// IP ACL authentication check
	if *ipACLEnabled {
		mapping := ipACLMapping.Load()
		if mapping == nil || len(*mapping) == 0 {
			log.Printf("Auth DENIED: IP ACL enabled but no valid mapping loaded")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		ip := clientIP(r, *ipACLForwarded)
		ok, reason := validateAuth(query, *mapping, ip)
		if !ok {
			log.Printf("Auth DENIED: %s", reason)
			if strings.HasPrefix(reason, "unknown IP") {
				w.WriteHeader(http.StatusUnauthorized)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}
		log.Printf("Auth ALLOWED: IP %s validated", ip)
	}

	var allContent [][]byte
	var foundQueryMatches bool

	// Add common config if specified and exists
	if *commonConfig != "" {
		if _, err := os.Stat(*commonConfig); err == nil {
			commonData, err := os.ReadFile(*commonConfig)
			if err != nil {
				log.Printf("Error reading common config %s: %v", *commonConfig, err)
			} else {
				allContent = append(allContent, commonData)
			}
		}
	}

	// Process all query parameters
	for paramName, values := range query {
		if len(values) == 0 || values[0] == "" {
			continue
		}

		paramValue := values[0]
		paramDir := filepath.Join(*dataDir, paramName)

		// Check if parameter directory exists
		if _, err := os.Stat(paramDir); os.IsNotExist(err) {
			log.Printf("Parameter directory %s does not exist, skipping", paramDir)
			continue
		}

		var matches []string
		// Special handling for MAC addresses (m parameter)
		if paramName == "m" {
			matches = findMACMatches(paramDir, paramValue)
		} else {
			matches = findExactMatches(paramDir, paramValue)
		}

		for _, match := range matches {
			content, err := readFiles(match)
			if err != nil {
				log.Printf("Error reading %s: %v", match, err)
				continue
			}
			allContent = append(allContent, content...)
			foundQueryMatches = true
		}
	}

	// If no query matches found, try default config
	if !foundQueryMatches {
		// Try default config if it exists
		if *defaultConfig != "" {
			if _, err := os.Stat(*defaultConfig); err == nil {
				defaultData, err := os.ReadFile(*defaultConfig)
				if err != nil {
					log.Printf("Error reading default config %s: %v", *defaultConfig, err)
					log.Printf("Response: status=404 (default config read error)")
					w.WriteHeader(http.StatusNotFound)
					return
				}
				allContent = append(allContent, defaultData)
			} else {
				log.Printf("Response: status=404 (default config not found)")
				w.WriteHeader(http.StatusNotFound)
				return
			}
		} else {
			log.Printf("Response: status=404 (no matches and no default config)")
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}

	// Combine all YAML documents with separator
	combined := combineYAMLDocuments(allContent)

	// Validate YAML syntax
	if err := validateYAML(combined); err != nil {
		log.Printf("Invalid YAML generated: %v", err)
		log.Printf("Response: status=500 (invalid YAML)")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Write(combined)
	log.Printf("Response: status=200 size=%d", len(combined))
}

// findExactMatches searches for exact matches (files or directories) case-insensitively
func findExactMatches(baseDir, value string) []string {
	var matches []string

	// Normalize the value (replace colons with hyphens for file system safety)
	normalized := normalizeValue(value)

	// Read directory contents
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return matches
	}

	// Search for case-insensitive matches
	for _, entry := range entries {
		entryName := entry.Name()
		entryNameLower := strings.ToLower(entryName)

		// Check for exact match (without extension)
		if entryNameLower == normalized {
			matches = append(matches, filepath.Join(baseDir, entryName))
			return matches
		}

		// Check for match with .yaml or .yml extension
		for _, ext := range []string{".yaml", ".yml"} {
			if entryNameLower == normalized+ext {
				matches = append(matches, filepath.Join(baseDir, entryName))
				return matches
			}
		}
	}

	return matches
}

// findMACMatches searches for MAC address matches from most specific to most general
func findMACMatches(baseDir, mac string) []string {
	// Normalize MAC address: remove colons, convert to lowercase
	normalized := strings.ToLower(strings.ReplaceAll(mac, ":", "-"))

	// Try progressively shorter prefixes
	parts := strings.Split(normalized, "-")

	for i := len(parts); i > 0; i-- {
		prefix := strings.Join(parts[:i], "-")

		// Try exact match
		matches := findExactMatches(baseDir, prefix)
		if len(matches) > 0 {
			log.Printf("Found MAC match: %s", prefix)
			return matches
		}
	}

	return nil
}

// readFiles reads content from a file or all files in a directory
func readFiles(path string) ([][]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return [][]byte{content}, nil
	}

	// Read all YAML files from directory
	var contents [][]byte
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		filePath := filepath.Join(path, name)
		content, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("Error reading %s: %v", filePath, err)
			continue
		}
		contents = append(contents, content)
	}

	return contents, nil
}

// combineYAMLDocuments combines multiple YAML documents with the --- separator
func combineYAMLDocuments(documents [][]byte) []byte {
	if len(documents) == 0 {
		return []byte{}
	}

	var result []byte
	for i, doc := range documents {
		// Trim whitespace
		doc = []byte(strings.TrimSpace(string(doc)))

		if i > 0 {
			// Add separator between documents
			result = append(result, []byte("\n---\n")...)
		}
		result = append(result, doc...)
	}
	result = append(result, '\n')

	return result
}

// validateYAML checks if the combined content is valid YAML
func validateYAML(content []byte) error {
	// Split by YAML document separator and validate each
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))

	for {
		var doc interface{}
		err := decoder.Decode(&doc)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return fmt.Errorf("invalid YAML: %w", err)
		}
	}

	return nil
}

// normalizeValue normalizes a value for use as a filename
func normalizeValue(value string) string {
	// Replace colons with hyphens (common for MAC addresses)
	value = strings.ReplaceAll(value, ":", "-")
	// Convert to lowercase for consistency
	value = strings.ToLower(value)
	return value
}

// validateAuth validates query parameters against the IP ACL mapping.
// Returns (true, "") if auth passes, or (false, reason) if it fails.
// Unknown params (not in mapping) are skipped; only known params are validated.
func validateAuth(query url.Values, mapping map[string]map[string]string, ip string) (bool, string) {
	expectedParams, ok := mapping[ip]
	if !ok {
		return false, fmt.Sprintf("unknown IP %s", ip)
	}

	var recognizedCount int
	for paramName, values := range query {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		paramValue := values[0]

		expectedValue, defined := expectedParams[paramName]
		if !defined {
			// Unknown params are skipped (ignored by handler anyway)
			continue
		}

		if normalizeValue(paramValue) != normalizeValue(expectedValue) {
			return false, fmt.Sprintf("param '%s' mismatch for IP %s", paramName, ip)
		}

		recognizedCount++
	}

	if recognizedCount == 0 {
		return false, fmt.Sprintf("no recognized parameters for IP %s", ip)
	}

	return true, ""
}
