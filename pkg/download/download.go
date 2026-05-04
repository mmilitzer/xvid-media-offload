package download

import (
	"context"
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
	apiBaseURL    = "https://api.xvid.com"
	signPath      = "/v1/files/downloads/"
	defaultExpiry = 30 * time.Minute
)

// Downloader is the interface for downloading files. It can be mocked in tests.
type Downloader interface {
	Download(signedURL string, destPath string) error
	DownloadContext(ctx context.Context, signedURL string, destPath string) error
}

// HTTPDownloader implements Downloader using the standard HTTP client.
type HTTPDownloader struct {
	Client *http.Client
}

// Download downloads the resource at signedURL to destPath.
func (d *HTTPDownloader) Download(signedURL string, destPath string) error {
	return d.DownloadContext(context.Background(), signedURL, destPath)
}

// DownloadContext downloads the resource at signedURL to destPath, respecting ctx.
func (d *HTTPDownloader) DownloadContext(ctx context.Context, signedURL string, destPath string) error {
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}

	resp, err := client.Do(req)
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

	key, err := decodeClientSecret(s.ClientSecret)
	if err != nil {
		return "", fmt.Errorf("decoding client_secret: %w", err)
	}

	h := hmac.New(sha256.New, key)
	h.Write([]byte(url))
	signature := fmt.Sprintf("%x", h.Sum(nil))

	signedURL := apiBaseURL + url + "&signature=" + signature
	return signedURL, nil
}

// decodeClientSecret attempts to decode a base64-encoded client secret,
// accepting standard, URL-safe, and unpadded variants.
func decodeClientSecret(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("unable to decode client_secret as base64")
}
