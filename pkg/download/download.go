package download

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const (
	apiBaseURL     = "https://api.xvid.com"
	signPath       = "/v1/files/downloads/"
	defaultExpiry  = 30 * time.Minute
)

// Downloader is the interface for downloading files. It can be mocked in tests.
type Downloader interface {
	Download(signedURL string, destPath string) error
}

// HTTPDownloader implements Downloader using the standard HTTP client.
type HTTPDownloader struct {
	Client *http.Client
}

// Download downloads the resource at signedURL to destPath.
func (d *HTTPDownloader) Download(signedURL string, destPath string) error {
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Get(signedURL)
	if err != nil {
		return fmt.Errorf("http GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating dest file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("writing dest file: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("fsync dest file: %w", err)
	}

	return nil
}

// Signer generates HMAC-signed temporary download URLs.
type Signer struct {
	ClientID     string
	ClientSecret string
}

// SignURL creates a signed download URL for the given fileID.
// If autograph is true, it adds expert_mode=true to the query.
func (s *Signer) SignURL(fileID string, autograph bool) (string, error) {
	if s.ClientID == "" || s.ClientSecret == "" {
		return "", fmt.Errorf("client_id and client_secret are required")
	}

	url := signPath + "?file_id=" + fileID + "&redirect=true"
	if autograph {
		url += "&expert_mode=true"
	}

	expires := time.Now().Add(defaultExpiry).Unix()
	url += "&client_id=" + s.ClientID + "&expiry_time=" + strconv.FormatInt(expires, 10)

	key, err := base64.StdEncoding.DecodeString(s.ClientSecret)
	if err != nil {
		return "", fmt.Errorf("decoding client_secret: %w", err)
	}

	h := hmac.New(sha256.New, key)
	h.Write([]byte(url))
	signature := fmt.Sprintf("%x", h.Sum(nil))

	signedURL := apiBaseURL + url + "&signature=" + signature
	return signedURL, nil
}
