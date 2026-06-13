package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

const geoHTTPTimeout = 30 * time.Second

type geoLookup struct {
	city *maxminddb.Reader
	asn  *maxminddb.Reader
}

type geoInfo struct {
	country string
	region  string
	city    string
	asn     int
	asOrg   string
	asType  string
}

func setupGeoFiles(dir string) {
	if os.Getenv("SKIP_BUILD_GEO") != "" {
		slog.Info("geo setup skipped", "reason", "SKIP_BUILD_GEO")
		return
	}
	if os.Getenv("VERCEL") != "" && os.Getenv("BUILD_GEO") == "" {
		slog.Info("geo setup skipped", "reason", "VERCEL without BUILD_GEO")
		return
	}
	if dir == "" {
		dir = "geo"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Warn("geo dir create failed", "dir", dir, "err", err)
		return
	}
	cityPath := filepath.Join(dir, "GeoLite2-City.mmdb")
	asnPath := filepath.Join(dir, "GeoLite2-ASN.mmdb")
	ensureGeoDB("GeoLite2-City", "GEO_DATABASE_URL", cityPath)
	ensureGeoDB("GeoLite2-ASN", "GEO_ASN_DATABASE_URL", asnPath)
}

func setupGeo(dir string) *geoLookup {
	if os.Getenv("SKIP_BUILD_GEO") != "" {
		slog.Info("geo load skipped", "reason", "SKIP_BUILD_GEO")
		return nil
	}
	if dir == "" {
		dir = "geo"
	}
	cityPath := filepath.Join(dir, "GeoLite2-City.mmdb")
	asnPath := filepath.Join(dir, "GeoLite2-ASN.mmdb")
	g := &geoLookup{}
	if db, err := maxminddb.Open(cityPath); err == nil {
		g.city = db
	} else {
		slog.Warn("geo city db unavailable", "path", cityPath, "err", err)
	}
	if db, err := maxminddb.Open(asnPath); err == nil {
		g.asn = db
	} else {
		slog.Warn("geo asn db unavailable", "path", asnPath, "err", err)
	}
	if g.city == nil && g.asn == nil {
		return nil
	}
	return g
}

func (g *geoLookup) close() {
	if g == nil {
		return
	}
	if g.city != nil {
		g.city.Close()
	}
	if g.asn != nil {
		g.asn.Close()
	}
}

func ensureGeoDB(db, envURL, path string) {
	if _, err := os.Stat(path); err == nil && os.Getenv("BUILD_GEO") == "" {
		return
	}
	url := os.Getenv(envURL)
	if url == "" && db == "GeoLite2-City" {
		url = os.Getenv("GEO_DATABASE_URL")
	}
	if url == "" {
		if key := os.Getenv("MAXMIND_LICENSE_KEY"); key != "" {
			url = fmt.Sprintf("https://download.maxmind.com/app/geoip_download?edition_id=%s&license_key=%s&suffix=tar.gz", db, neturl.QueryEscape(key))
		} else {
			url = fmt.Sprintf("https://raw.githubusercontent.com/GitSquared/node-geolite2-redist/master/redist/%s.tar.gz", db)
		}
	}
	if strings.HasSuffix(strings.Split(url, "?")[0], ".mmdb") {
		if err := downloadDirectMMDB(url, path); err != nil {
			slog.Warn("geo direct download failed", "db", db, "url", url, "err", err)
		}
		return
	}
	if err := downloadTarMMDB(url, path); err != nil {
		slog.Warn("geo archive download failed", "db", db, "url", url, "err", err)
	}
}

func downloadDirectMMDB(url, path string) error {
	res, err := httpClient().Get(url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("status %s", res.Status)
	}
	return writeFile(path, res.Body)
}

func downloadTarMMDB(url, path string) error {
	res, err := httpClient().Get(url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("status %s", res.Status)
	}
	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no mmdb in archive")
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && strings.HasSuffix(h.Name, ".mmdb") {
			return writeFile(path, tr)
		}
	}
}

func writeFile(path string, r io.Reader) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, path)
}

func httpClient() *http.Client { return &http.Client{Timeout: geoHTTPTimeout} }

func (g *geoLookup) lookup(ip string) geoInfo {
	if g == nil {
		return geoInfo{}
	}
	parsed := net.ParseIP(canonicalIPString(ip))
	if parsed == nil {
		return geoInfo{}
	}
	info := geoInfo{}
	if g.city != nil {
		var city struct {
			Country struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
			City struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"city"`
			Subdivisions []struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"subdivisions"`
		}
		if err := g.city.Lookup(parsed, &city); err == nil {
			info.country = city.Country.ISOCode
			info.city = city.City.Names["en"]
			if len(city.Subdivisions) > 0 {
				info.region = city.Subdivisions[0].Names["en"]
			}
		}
	}
	if g.asn != nil {
		var asn struct {
			Number uint   `maxminddb:"autonomous_system_number"`
			Org    string `maxminddb:"autonomous_system_organization"`
		}
		if err := g.asn.Lookup(parsed, &asn); err == nil {
			info.asn = int(asn.Number)
			info.asOrg = asn.Org
			info.asType = classifyAS(asn.Org)
		}
	}
	return info
}

func classifyAS(org string) string {
	s := strings.ToLower(org)
	switch {
	case strings.Contains(s, "university") || strings.Contains(s, "college") || strings.Contains(s, "school") || strings.Contains(s, "research"):
		return "university"
	case strings.Contains(s, "cloud") || strings.Contains(s, "hosting") || strings.Contains(s, "data") || strings.Contains(s, "server") || strings.Contains(s, "digitalocean") || strings.Contains(s, "amazon") || strings.Contains(s, "google") || strings.Contains(s, "microsoft") || strings.Contains(s, "hetzner") || strings.Contains(s, "ovh"):
		return "datacenter"
	case strings.Contains(s, "telecom") || strings.Contains(s, "broadband") || strings.Contains(s, "cable") || strings.Contains(s, "dsl") || strings.Contains(s, "fiber") || strings.Contains(s, "mobile"):
		return "residential"
	case org != "":
		return "business"
	default:
		return ""
	}
}
