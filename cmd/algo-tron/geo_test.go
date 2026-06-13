package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyAS(t *testing.T) {
	cases := map[string]string{
		"Stanford University":          "university",
		"National Research Network":    "university", // "research"
		"Google LLC":                   "datacenter", // "google"
		"DigitalOcean, LLC":            "datacenter",
		"Hetzner Online GmbH":          "datacenter",
		"Amazon.com, Inc.":             "datacenter",
		"Comcast Cable Communications": "residential", // "cable"
		"Verizon Telecom":              "residential", // "telecom"
		"Some Mobile Network":          "residential", // "mobile"
		"Acme Widgets Inc":             "business",    // non-empty, no keyword
		"":                             "",
	}
	for org, want := range cases {
		if got := classifyAS(org); got != want {
			t.Errorf("classifyAS(%q) = %q, want %q", org, got, want)
		}
	}
}

// Datacenter keywords are checked before residential ones, so an org matching
// both ("Mobile Cloud") classifies as datacenter — guard that precedence.
func TestClassifyASDatacenterBeatsResidential(t *testing.T) {
	if got := classifyAS("Mobile Cloud Hosting"); got != "datacenter" {
		t.Errorf("classifyAS = %q, want datacenter (precedence over residential)", got)
	}
}

// A nil geoLookup (no databases loaded) must degrade to empty info, never panic.
func TestGeoLookupNilSafe(t *testing.T) {
	var g *geoLookup
	if info := g.lookup("1.2.3.4"); info != (geoInfo{}) {
		t.Errorf("nil geoLookup.lookup = %+v, want zero geoInfo", info)
	}
	g.close() // must also be nil-safe
}

func TestGeoLookupInvalidIP(t *testing.T) {
	g := &geoLookup{} // no readers
	if info := g.lookup("not-an-ip"); info != (geoInfo{}) {
		t.Errorf("lookup of invalid IP = %+v, want zero geoInfo", info)
	}
}

// — download / extraction ——————————————————————————————————————————————
//
// These exercise the real HTTP + gzip/tar code paths against a local httptest
// server, so they're fast and reproducible — no MaxMind credentials or network
// needed.

// serveBytes starts a throwaway HTTP server that returns the given status and
// body for any request, and returns its URL.
func serveBytes(t *testing.T, status int, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// makeTarGz builds an in-memory .tar.gz from name→content entries.
func makeTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestDownloadDirectMMDB(t *testing.T) {
	want := []byte("fake-mmdb-content")
	path := filepath.Join(t.TempDir(), "out.mmdb")

	if err := downloadDirectMMDB(serveBytes(t, 200, want), path); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("downloaded %q, want %q", got, want)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind after a successful download")
	}
}

func TestDownloadDirectMMDBHTTPError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mmdb")
	if err := downloadDirectMMDB(serveBytes(t, 500, []byte("nope")), path); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("no file should be written when the download fails")
	}
}

func TestDownloadTarMMDBExtractsDatabase(t *testing.T) {
	want := []byte("city-database-bytes")
	archive := makeTarGz(t, map[string][]byte{
		"GeoLite2-City_20240101/README.txt":         []byte("ignore me"),
		"GeoLite2-City_20240101/GeoLite2-City.mmdb": want,
	})
	path := filepath.Join(t.TempDir(), "city.mmdb")

	if err := downloadTarMMDB(serveBytes(t, 200, archive), path); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted %q, want %q (must pick the .mmdb entry, skip the README)", got, want)
	}
}

func TestDownloadTarMMDBNoDatabaseInArchive(t *testing.T) {
	archive := makeTarGz(t, map[string][]byte{"README.txt": []byte("no db here")})
	path := filepath.Join(t.TempDir(), "city.mmdb")
	if err := downloadTarMMDB(serveBytes(t, 200, archive), path); err == nil {
		t.Fatal("expected an error when the archive contains no .mmdb file")
	}
}

func TestDownloadTarMMDBHTTPError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "city.mmdb")
	if err := downloadTarMMDB(serveBytes(t, 404, nil), path); err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}
