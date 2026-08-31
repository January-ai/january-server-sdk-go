package main

import (
	"bufio"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type safeError string

func (e safeError) Error() string { return string(e) }

type config struct {
	key, query, upc, restaurantQuery, imagePath string
	latitude, longitude                         float64
	timeout                                     time.Duration
}

// parseEnv parses data, never shell syntax: no eval, expansion, sourcing, or process env mutation.
func parseEnv(data string) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	identifier := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || !identifier.MatchString(key) {
			return nil, safeError("invalid_env_file")
		}
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			end := -1
			for i := 1; i < len(value); i++ {
				if value[i] == '\\' && quote == '"' {
					i++
					continue
				}
				if value[i] == quote {
					end = i
					break
				}
			}
			if end < 0 {
				return nil, safeError("invalid_env_file")
			}
			tail := strings.TrimSpace(value[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, safeError("invalid_env_file")
			}
			value = value[1:end]
			if quote == '"' {
				value = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n", `\r`, "\r", `\t`, "\t").Replace(value)
			}
		} else {
			for i := 0; i < len(value); i++ {
				if value[i] == '#' && (i == 0 || value[i-1] == ' ' || value[i-1] == '\t') {
					value = strings.TrimSpace(value[:i])
					break
				}
			}
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return nil, safeError("invalid_env_file")
	}
	return values, nil
}

func loadConfig(root string, lookup func(string) (string, bool)) (config, error) {
	path := filepath.Join(root, ".env")
	explicit, hasExplicit := lookup("JANUARY_ENV_FILE")
	if hasExplicit && explicit != "" {
		path = explicit
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
	}
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil && (!errors.Is(err, os.ErrNotExist) || hasExplicit && explicit != "") {
		return config{}, safeError("env_file_unreadable")
	}
	if err == nil {
		values, err = parseEnv(string(data))
		if err != nil {
			return config{}, err
		}
	}
	get := func(key, def string) string {
		if v, ok := lookup(key); ok {
			return v
		}
		if v, ok := values[key]; ok {
			return v
		}
		return def
	}
	c := config{key: strings.TrimSpace(get("JANUARY_API_KEY", "")), query: get("JANUARY_E2E_QUERY", "banana"), upc: get("JANUARY_E2E_UPC", "049000006346"), restaurantQuery: get("JANUARY_E2E_RESTAURANT_QUERY", "chicken"), imagePath: get("JANUARY_E2E_IMAGE_PATH", "examples/live/food.png")}
	if c.key == "" {
		return config{}, safeError("missing_api_key")
	}
	if strings.HasPrefix(c.key, "ct-") || strings.ContainsAny(c.key, "\r\n") {
		return config{}, safeError("invalid_server_key")
	}
	seconds, err := strconv.ParseFloat(get("JANUARY_E2E_TIMEOUT_SECONDS", "120"), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 3600 {
		return config{}, safeError("invalid_timeout")
	}
	c.timeout = time.Duration(seconds * float64(time.Second))
	c.latitude, err = strconv.ParseFloat(get("JANUARY_E2E_LATITUDE", "37.7749"), 64)
	if err != nil || math.IsNaN(c.latitude) || math.Abs(c.latitude) > 90 {
		return config{}, safeError("invalid_latitude")
	}
	c.longitude, err = strconv.ParseFloat(get("JANUARY_E2E_LONGITUDE", "-122.4194"), 64)
	if err != nil || math.IsNaN(c.longitude) || math.Abs(c.longitude) > 180 {
		return config{}, safeError("invalid_longitude")
	}
	if !filepath.IsAbs(c.imagePath) {
		c.imagePath = filepath.Join(root, c.imagePath)
	}
	return c, nil
}
