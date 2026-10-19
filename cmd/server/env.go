package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// loadEnvFile sets variables from a KEY=value file (blank lines and # comments
// skipped, optional quotes around the value). Variables already set in the
// environment win. A missing file is not an error. It returns the keys it set.
func loadEnvFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var set []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok || key == "" {
			return set, fmt.Errorf("%s:%d: want KEY=value", path, n)
		}
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); exists || val == "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return set, err
		}
		set = append(set, key)
	}
	return set, sc.Err()
}
