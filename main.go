package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	// version is set via ldflags at build time
	version = "dev"

	dataDir       = flag.String("data-dir", "./data", "Directory containing metadata files")
	port          = flag.String("port", "8080", "Port to listen on")
	commonConfig  = flag.String("common-config", "", "Path to common configuration file (applied to every request)")
	defaultConfig = flag.String("default-config", "", "Path to default configuration file (returned for unmatched requests)")
)

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

	http.HandleFunc("/metadata", metadataHandler)

	addr := fmt.Sprintf(":%s", *port)
	log.Printf("Starting server on %s, data directory: %s", addr, *dataDir)
	log.Printf("Common config: %s, Default config: %s", *commonConfig, *defaultConfig)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
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
