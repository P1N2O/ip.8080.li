package main

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const ipsumURL = "https://raw.githubusercontent.com/stamparm/ipsum/master/ipsum.txt"

type securityDB struct {
	mu         sync.RWMutex
	malicious  map[string]int // IP → threat level (1–3)
	lastUpdate time.Time
}

var ipsumDB = &securityDB{malicious: make(map[string]int)}

func (db *securityDB) refresh() error {
	resp, err := http.Get(ipsumURL)
	if err != nil {
		return fmt.Errorf("ipsum download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("ipsum: HTTP %d", resp.StatusCode)
	}

	newSet := make(map[string]int)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			level := 0
			if len(fields[1]) == 1 && fields[1][0] >= '1' && fields[1][0] <= '3' {
				level = int(fields[1][0] - '0')
			}
			newSet[fields[0]] = level
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ipsum scan: %w", err)
	}

	db.mu.Lock()
	db.malicious = newSet
	db.lastUpdate = time.Now()
	db.mu.Unlock()
	return nil
}

func (db *securityDB) lookup(ip string) (level int, known bool) {
	db.mu.RLock()
	level, known = db.malicious[ip]
	db.mu.RUnlock()
	return
}


