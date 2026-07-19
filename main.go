package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"bufio"

	"github.com/oschwald/maxminddb-golang/v2"
)

// ─── Config ─────────────────────────────────────────────────────────────────

type config struct {
	port            string
	dbPath          string
	accountID       string
	licenseKey      string
	editionIDs      []string
	updateInterval  time.Duration
	poweredBy       string
	debug           bool
}

func loadConfig() *config {
	cfg := &config{
		port:       getEnv("PORT", "3000"),
		dbPath:     getEnv("GEOIPUPDATE_DB_PATH", "/app/.data/db"),
		accountID:  os.Getenv("GEOIPUPDATE_ACCOUNT_ID"),
		licenseKey: os.Getenv("GEOIPUPDATE_LICENSE_KEY"),
		poweredBy:  getEnv("POWERED_BY", ""),
		debug:      getEnv("DEBUG", "true") == "true",
	}

	if ids := os.Getenv("GEOIPUPDATE_EDITION_IDS"); ids != "" {
		for _, id := range strings.Fields(ids) {
			cfg.editionIDs = append(cfg.editionIDs, id)
		}
	}

	if freq := os.Getenv("GEOIPUPDATE_FREQUENCY"); freq != "" {
		if h, err := strconv.Atoi(freq); err == nil {
			cfg.updateInterval = time.Duration(h) * time.Hour
		}
	}
	if cfg.updateInterval == 0 {
		cfg.updateInterval = 168 * time.Hour // default: weekly
	}

	return cfg
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ─── MMDB Records ───────────────────────────────────────────────────────────

type asnRecord struct {
	Number       uint   `maxminddb:"autonomous_system_number"`
	Organization string `maxminddb:"autonomous_system_organization"`
}

type cityRecord struct {
	Continent struct {
		Code  string            `maxminddb:"code"`
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"continent"`
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Postal struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"postal"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
		TimeZone  string  `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

// ─── Server State ───────────────────────────────────────────────────────────

type server struct {
	cfg        *config
	asnReader  atomic.Pointer[maxminddb.Reader]
	cityReader atomic.Pointer[maxminddb.Reader]
	logf       func(format string, v ...any)
}

func newServer(cfg *config) *server {
	s := &server{cfg: cfg}
	if cfg.debug {
		s.logf = log.Printf
	} else {
		s.logf = func(string, ...any) {}
	}
	return s
}

func (s *server) openReaders() error {
	asnPath := filepath.Join(s.cfg.dbPath, "GeoLite2-ASN.mmdb")
	cityPath := filepath.Join(s.cfg.dbPath, "GeoLite2-City.mmdb")

	if newReader, err := openReader(asnPath); err != nil {
		s.logf("warn: %s: %v (ASN lookups disabled)", asnPath, err)
	} else if old := s.asnReader.Swap(newReader); old != nil {
		old.Close()
	}

	if newReader, err := openReader(cityPath); err != nil {
		s.logf("warn: %s: %v (city lookups disabled)", cityPath, err)
	} else if old := s.cityReader.Swap(newReader); old != nil {
		old.Close()
	}

	// Invalidate cache since readers changed
	sharedCache.clear()
	return nil
}

func openReader(path string) (*maxminddb.Reader, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return maxminddb.Open(path)
}

// ─── Geo Cache ────────────────────────────────────────────────────────────────

const cacheTTL = 5 * time.Minute
const cacheMax = 1000

type cacheEntry struct {
	resp      geoResponse
	expiresAt time.Time
}

type geoCache struct {
	mu    sync.RWMutex
	items map[string]*cacheEntry
}

func newGeoCache() *geoCache {
	return &geoCache{items: make(map[string]*cacheEntry)}
}

func (c *geoCache) get(ip string) (geoResponse, bool) {
	c.mu.RLock()
	e, ok := c.items[ip]
	c.mu.RUnlock()
	if !ok {
		return geoResponse{}, false
	}
	if time.Now().After(e.expiresAt) {
		c.mu.Lock()
		delete(c.items, ip)
		c.mu.Unlock()
		return geoResponse{}, false
	}
	return e.resp, true
}

func (c *geoCache) set(ip string, resp geoResponse) {
	c.mu.Lock()
	if len(c.items) >= cacheMax {
		// Evict one random entry (good enough for 1000 entries)
		for k := range c.items {
			delete(c.items, k)
			break
		}
	}
	c.items[ip] = &cacheEntry{resp: resp, expiresAt: time.Now().Add(cacheTTL)}
	c.mu.Unlock()
}

func (c *geoCache) clear() {
	c.mu.Lock()
	c.items = make(map[string]*cacheEntry)
	c.mu.Unlock()
}

var sharedCache = newGeoCache()

// ─── IPsum Threat Feed ────────────────────────────────────────────────────

// ponytail: simple in-memory map, full replace on refresh, no per-entry eviction
var ipsumClient = &http.Client{Timeout: 30 * time.Second}

const ipsumURL = "https://raw.githubusercontent.com/stamparm/ipsum/master/ipsum.txt"

type ipsumList struct {
	mu    sync.RWMutex
	items map[netip.Addr]int
}

var ipsumData = &ipsumList{items: make(map[netip.Addr]int)}

func (l *ipsumList) get(addr netip.Addr) int {
	l.mu.RLock()
	score := l.items[addr]
	l.mu.RUnlock()
	return score
}

func (s *server) fetchIPsum() error {
	resp, err := ipsumClient.Get(ipsumURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	m := make(map[netip.Addr]int)
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		if n > 0 {
			m[addr] = n
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	ipsumData.mu.Lock()
	ipsumData.items = m
	ipsumData.mu.Unlock()
	return nil
}

func (s *server) runIPsumFetcher(ctx context.Context) {
	// Initial fetch
	if err := s.fetchIPsum(); err != nil {
		s.logf("ipsum: initial fetch failed: %v", err)
	}

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.fetchIPsum(); err != nil {
				s.logf("ipsum: fetch failed: %v", err)
			}
		}
	}
}

func (s *server) lookup(ipStr string) *geoResponse {
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return nil
	}

	// Cache hit
	if cached, ok := sharedCache.get(ipStr); ok {
		return &cached
	}

	asnR := s.asnReader.Load()
	cityR := s.cityReader.Load()

	resp := &geoResponse{IP: ipStr}

	// ponytail: check threat feed alongside geo
	if score := ipsumData.get(addr); score > 0 {
		resp.ThreatScore = score
	}

	if asnR == nil && cityR == nil {
		sharedCache.set(ipStr, *resp)
		return resp
	}

	if asnR != nil {
		var a asnRecord
		if err := asnR.Lookup(addr).Decode(&a); err == nil && a.Number > 0 {
			resp.ASN = a.Number
			resp.ASOrganization = a.Organization
		}
	}

	if cityR != nil {
		var c cityRecord
		if err := cityR.Lookup(addr).Decode(&c); err == nil {
			if c.Continent.Code != "" {
				resp.ContinentCode = c.Continent.Code
				if names, ok := c.Continent.Names["en"]; ok {
					resp.Continent = names
				}
			}
			if c.Country.ISOCode != "" {
				resp.CountryCode = c.Country.ISOCode
				if names, ok := c.Country.Names["en"]; ok {
					resp.Country = names
				}
			}
			if len(c.Subdivisions) > 0 {
				resp.RegionCode = c.Subdivisions[0].ISOCode
				if names, ok := c.Subdivisions[0].Names["en"]; ok {
					resp.Region = names
				}
			}
			if len(c.City.Names) > 0 {
				if names, ok := c.City.Names["en"]; ok {
					resp.City = names
				}
			}
			if c.Postal.Code != "" {
				resp.PostalCode = c.Postal.Code
			}
			if c.Location.Latitude != 0 || c.Location.Longitude != 0 {
				resp.Latitude = c.Location.Latitude
				resp.Longitude = c.Location.Longitude
				resp.Timezone = c.Location.TimeZone
			}
		}
	}

	sharedCache.set(ipStr, *resp)
	return resp
}

// ─── Background Updater ─────────────────────────────────────────────────────

func (s *server) runUpdater(ctx context.Context) {
	// Initial download if missing
	if err := s.updateDatabases(); err != nil {
		s.logf("initial DB download: %v", err)
	}
	s.openReaders()

	ticker := time.NewTicker(s.cfg.updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.logf("checking for DB updates...")
			if err := s.updateDatabases(); err != nil {
				s.logf("DB update failed: %v", err)
			} else {
				s.openReaders()
			}
		}
	}
}

func (s *server) updateDatabases() error {
	if s.cfg.accountID == "" || s.cfg.licenseKey == "" {
		return fmt.Errorf("GEOIPUPDATE_ACCOUNT_ID and GEOIPUPDATE_LICENSE_KEY required")
	}
	if len(s.cfg.editionIDs) == 0 {
		return nil
	}

	_ = os.MkdirAll(s.cfg.dbPath, 0755)

	var wg sync.WaitGroup
	for _, id := range s.cfg.editionIDs {
		wg.Add(1)
		go func(editionID string) {
			defer wg.Done()

			filePath := filepath.Join(s.cfg.dbPath, editionID+".mmdb")

			// Compute current hash
			var currentMD5 string
			if data, err := os.ReadFile(filePath); err == nil {
				h := md5.Sum(data)
				currentMD5 = fmt.Sprintf("%x", h)
			}

			needsUpdate, date, newMD5, err := s.checkMetadata(editionID, currentMD5)
			if err != nil {
				s.logf("  %s: metadata check failed: %v", editionID, err)
				return
			}
			if !needsUpdate {
				s.logf("  %s: up to date", editionID)
				return
			}

			s.logf("  %s: downloading update...", editionID)
			if err := s.downloadEdition(editionID, date, filePath); err != nil {
				s.logf("  %s: download failed: %v", editionID, err)
				return
			}

			// Verify MD5
			if data, err := os.ReadFile(filePath); err == nil {
				h := md5.Sum(data)
				actual := fmt.Sprintf("%x", h)
				if newMD5 != "" && actual != newMD5 {
					s.logf("  %s: MD5 mismatch (expected %s, got %s)", editionID, newMD5, actual)
				} else {
					s.logf("  %s: updated", editionID)
				}
			}
		}(id)
	}
	wg.Wait()
	return nil
}

// ponytail: single client with timeout for all MaxMind API calls
var updaterClient = &http.Client{Timeout: 30 * time.Second}

func (s *server) checkMetadata(editionID, currentMD5 string) (needsUpdate bool, date string, newMD5 string, err error) {
	endpoint := fmt.Sprintf("https://updates.maxmind.com/geoip/updates/metadata?edition_id=%s", url.QueryEscape(editionID))
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.SetBasicAuth(s.cfg.accountID, s.cfg.licenseKey)
	req.Header.Set("User-Agent", "ip.8080.li/1.0")

	resp, err := updaterClient.Do(req)
	if err != nil {
		return false, "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return false, "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var meta struct {
		Databases []struct {
			Date string `json:"date"`
			MD5  string `json:"md5"`
		} `json:"databases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return false, "", "", err
	}
	if len(meta.Databases) != 1 {
		return false, "", "", fmt.Errorf("expected 1 database, got %d", len(meta.Databases))
	}

	db := meta.Databases[0]
	if currentMD5 != "" && currentMD5 == db.MD5 {
		return false, "", "", nil
	}
	return true, db.Date, db.MD5, nil
}

func (s *server) downloadEdition(editionID, date, filePath string) error {
	date = strings.ReplaceAll(date, "-", "")
	params := url.Values{"date": {date}, "suffix": {"tar.gz"}}
	endpoint := fmt.Sprintf("https://updates.maxmind.com/geoip/databases/%s/download?%s", url.PathEscape(editionID), params.Encode())

	req, _ := http.NewRequest("GET", endpoint, nil)
	req.SetBasicAuth(s.cfg.accountID, s.cfg.licenseKey)
	req.Header.Set("User-Agent", "ip.8080.li/1.0")

	resp, err := updaterClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no .mmdb file in archive")
		}
		if err != nil {
			return err
		}
		if strings.HasSuffix(hdr.Name, ".mmdb") {
			tmpPath := filePath + ".tmp"
			f, err := os.Create(tmpPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				os.Remove(tmpPath)
				return err
			}
			f.Close()
			return os.Rename(tmpPath, filePath)
		}
	}
}

// ─── Flag Emoji ─────────────────────────────────────────────────────────────

func countryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	// Regional Indicator Symbol A = U+1F1E6, offset = 0x1F1E6 - 'A'
	r1 := rune(code[0]) + 0x1F1E6 - 'A'
	r2 := rune(code[1]) + 0x1F1E6 - 'A'
	if r1 < 0x1F1E6 || r1 > 0x1F1FF || r2 < 0x1F1E6 || r2 > 0x1F1FF {
		return ""
	}
	return string([]rune{r1, r2})
}

// ─── HTTP Handlers ──────────────────────────────────────────────────────────

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func gzipMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next(&gzipResponseWriter{Writer: gz, ResponseWriter: w}, r)
	}
}

type geoResponse struct {
	IP               string  `json:"ip"`
	Flag             string  `json:"flag,omitempty"`
	ContinentCode    string  `json:"continentCode,omitempty"`
	Continent        string  `json:"continent,omitempty"`
	CountryCode      string  `json:"countryCode,omitempty"`
	Country          string  `json:"country,omitempty"`
	RegionCode       string  `json:"regionCode,omitempty"`
	Region           string  `json:"region,omitempty"`
	City             string  `json:"city,omitempty"`
	PostalCode       string  `json:"postalCode,omitempty"`
	Latitude         float64 `json:"latitude,omitempty"`
	Longitude        float64 `json:"longitude,omitempty"`
	Timezone         string  `json:"timezone,omitempty"`
	ASN              uint    `json:"asn,omitempty"`
	ASOrganization   string  `json:"asOrganization,omitempty"`
	ThreatScore      int     `json:"threatScore"`
}

func (s *server) handleRequest(w http.ResponseWriter, r *http.Request) {
	// Parse format
	format := r.URL.Query().Get("format")
	if format == "" {
		format = r.URL.Query().Get("fmt")
	}
	if format == "" {
		if ext := strings.TrimPrefix(r.URL.Path, "/geo"); ext != "" {
			format = strings.TrimPrefix(ext, ".")
		}
	}
	if format == "" {
		format = "text"
	}

	callback := r.URL.Query().Get("callback")
	if callback == "" {
		callback = r.URL.Query().Get("cb")
	}
	if callback == "" {
		callback = "callback"
	}

	// Client IP
	clientIP := r.Header.Get("CF-Connecting-IPv6")
	if clientIP == "" {
		clientIP = r.Header.Get("CF-Connecting-IP")
	}
	if clientIP == "" {
		clientIP = r.Header.Get("X-Forwarded-For")
	}
	if clientIP == "" {
		clientIP = r.Header.Get("X-Real-IP")
	}
	if clientIP == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			clientIP = host
		}
	}
	if clientIP == "" {
		clientIP = "127.0.0.1"
	}

	ip := r.URL.Query().Get("ip")
	if ip == "" {
		ip = clientIP
	}

	showDetails := r.URL.Path != "/" && r.URL.Path != "/ip" || r.URL.Query().Has("ip")

	if s.cfg.debug {
		log.Printf("[%s] %s request from %s", time.Now().Format(time.RFC3339), r.Method, clientIP)
	}

	// Build response
	resp := geoResponse{IP: ip}
	if showDetails {
		if raw := s.lookup(ip); raw != nil {
			resp = *raw

			// Fallback to Cloudflare headers
			if resp.CountryCode == "" {
				resp.CountryCode = r.Header.Get("CF-IPCountry")
			}
			if resp.ContinentCode == "" {
				resp.ContinentCode = r.Header.Get("CF-IPContinent")
			}
			if resp.City == "" {
				resp.City = r.Header.Get("CF-IPCity")
			}
			if resp.PostalCode == "" {
				resp.PostalCode = r.Header.Get("CF-Postal-Code")
			}
			if resp.Latitude == 0 {
				resp.Latitude, _ = strconv.ParseFloat(r.Header.Get("CF-IPLatitude"), 64)
			}
			if resp.Longitude == 0 {
				resp.Longitude, _ = strconv.ParseFloat(r.Header.Get("CF-IPLongitude"), 64)
			}
			if resp.Timezone == "" {
				resp.Timezone = r.Header.Get("CF-Timezone")
			}
			if resp.ASN == 0 {
				if asn := r.Header.Get("X-ASN"); asn != "" {
					if n, err := strconv.ParseUint(asn, 10, 32); err == nil {
						resp.ASN = uint(n)
					}
				}
			}
			if resp.ASOrganization == "" {
				resp.ASOrganization = r.Header.Get("CF-ASOrganization")
			}
			if resp.RegionCode == "" && resp.CountryCode != "" {
				resp.RegionCode = r.Header.Get("CF-Region-Code")
				resp.Region = r.Header.Get("CF-Region")
			}
			if resp.RegionCode == "" {
				if ray := r.Header.Get("CF-Ray"); ray != "" {
					if parts := strings.SplitN(ray, "-", 2); len(parts) == 2 {
						resp.RegionCode = parts[1]
					}
				}
			}

			if resp.CountryCode != "" {
				resp.Flag = countryFlag(resp.CountryCode)
			}
		}
	}

	// Common headers
	headers := w.Header()
	headers.Set("Connection", "keep-alive")
	headers.Set("Access-Control-Allow-Origin", "*")
	headers.Set("Cache-Control", "public, max-age=60, s-maxage=300")

	poweredBy := s.cfg.poweredBy
	if poweredBy == "" {
		poweredBy = r.Host
	}
	headers.Set("X-Powered-By", poweredBy)
	headers.Set("X-Client-IP", clientIP)

	// Favicon
	if strings.Contains(r.URL.Path, "/favicon.ico") {
		headers.Set("Content-Type", "image/svg+xml")
		w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><text x="50" y="50" font-size="90" text-anchor="middle" dominant-baseline="central">🌐</text></svg>`))
		return
	}

	switch format {
	case "json":
		headers.Set("Content-Type", "application/json")
		b, _ := json.Marshal(resp)
		w.Write(b)
		w.Write([]byte("\n"))

	case "jsonp":
		headers.Set("Content-Type", "application/javascript")
		b, _ := json.Marshal(resp)
		w.Write([]byte(callback + "("))
		w.Write(b)
		w.Write([]byte(");\n"))

	case "xml":
		headers.Set("Content-Type", "application/xml")
		serializeXML(w, resp)

	default: // text
		headers.Set("Content-Type", "text/plain; charset=utf-8")
		serializeText(w, resp)
	}
}

func serializeText(w io.Writer, r geoResponse) {
	if r.IP != "" && r.Flag == "" && r.CountryCode == "" && r.Country == "" &&
		r.City == "" && r.ASN == 0 {
		fmt.Fprintln(w, r.IP)
		return
	}
	// JSON/struct field order (matches json output)
	fmt.Fprintf(w, "ip: %s\n", r.IP)
	if r.Flag != "" {
		fmt.Fprintf(w, "flag: %s\n", r.Flag)
	}
	if r.ContinentCode != "" {
		fmt.Fprintf(w, "continentCode: %s\n", r.ContinentCode)
	}
	if r.Continent != "" {
		fmt.Fprintf(w, "continent: %s\n", r.Continent)
	}
	if r.CountryCode != "" {
		fmt.Fprintf(w, "countryCode: %s\n", r.CountryCode)
	}
	if r.Country != "" {
		fmt.Fprintf(w, "country: %s\n", r.Country)
	}
	if r.RegionCode != "" {
		fmt.Fprintf(w, "regionCode: %s\n", r.RegionCode)
	}
	if r.Region != "" {
		fmt.Fprintf(w, "region: %s\n", r.Region)
	}
	if r.City != "" {
		fmt.Fprintf(w, "city: %s\n", r.City)
	}
	if r.PostalCode != "" {
		fmt.Fprintf(w, "postalCode: %s\n", r.PostalCode)
	}
	if r.Latitude != 0 || r.Longitude != 0 {
		fmt.Fprintf(w, "latitude: %.6f\n", r.Latitude)
		fmt.Fprintf(w, "longitude: %.6f\n", r.Longitude)
	}
	if r.Timezone != "" {
		fmt.Fprintf(w, "timezone: %s\n", r.Timezone)
	}
	if r.ASN > 0 {
		fmt.Fprintf(w, "asn: %d\n", r.ASN)
	}
	if r.ASOrganization != "" {
		fmt.Fprintf(w, "asOrganization: %s\n", r.ASOrganization)
	}
	fmt.Fprintf(w, "threatScore: %d\n", r.ThreatScore)
}

func serializeXML(w io.Writer, r geoResponse) {
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n<response>\n")
	// Simple XML serialization
	writeXMLField(w, "ip", r.IP, 1)
	writeXMLField(w, "flag", r.Flag, 1)
	writeXMLField(w, "continentCode", r.ContinentCode, 1)
	writeXMLField(w, "continent", r.Continent, 1)
	writeXMLField(w, "countryCode", r.CountryCode, 1)
	writeXMLField(w, "country", r.Country, 1)
	writeXMLField(w, "regionCode", r.RegionCode, 1)
	writeXMLField(w, "region", r.Region, 1)
	writeXMLField(w, "city", r.City, 1)
	writeXMLField(w, "postalCode", r.PostalCode, 1)
	if r.Latitude != 0 || r.Longitude != 0 {
		writeXMLField(w, "latitude", fmt.Sprintf("%.6f", r.Latitude), 1)
		writeXMLField(w, "longitude", fmt.Sprintf("%.6f", r.Longitude), 1)
	}
	writeXMLField(w, "timezone", r.Timezone, 1)
	if r.ASN > 0 {
		writeXMLField(w, "asn", strconv.FormatUint(uint64(r.ASN), 10), 1)
	}
	writeXMLField(w, "asOrganization", r.ASOrganization, 1)
	fmt.Fprintf(w, "  <%s>", "threatScore")
	xml.EscapeText(w, []byte(strconv.FormatInt(int64(r.ThreatScore), 10)))
	fmt.Fprintf(w, "</%s>\n", "threatScore")
	io.WriteString(w, "</response>\n")
}

func writeXMLField(w io.Writer, tag, value string, indent int) {
	if value == "" || value == "0" {
		return
	}
	xml.EscapeText(w, []byte(strings.Repeat("  ", indent)))
	fmt.Fprintf(w, "<%s>", xmlProcInst(tag))
	xml.EscapeText(w, []byte(value))
	fmt.Fprintf(w, "</%s>\n", xmlProcInst(tag))
}

// ponytail: xmlProcInst is not a real procinst, just a helper name
func xmlProcInst(s string) string { return s }

// ─── Main ───────────────────────────────────────────────────────────────────

func main() {
	cfg := loadConfig()
	srv := newServer(cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start background updater
	go srv.runUpdater(ctx)

	// Start IPsum threat feed fetcher
	go srv.runIPsumFetcher(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", gzipMiddleware(srv.handleRequest))

	httpServer := &http.Server{
		Addr:    ":" + cfg.port,
		Handler: mux,
	}

	go func() {
		log.Printf("🚀 Server running at http://0.0.0.0:%s", cfg.port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(shutdownCtx)
}
