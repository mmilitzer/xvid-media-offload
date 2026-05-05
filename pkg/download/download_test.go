package download

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSignURLBasic(t *testing.T) {
	signer := &Signer{
		ClientID:     "test-client-id",
		ClientSecret: base64.StdEncoding.EncodeToString([]byte("test-secret-key")),
	}

	signedURL, err := signer.SignURL("file123", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(signedURL, apiBaseURL) {
		t.Errorf("expected prefix %s, got %s", apiBaseURL, signedURL)
	}
	if !strings.Contains(signedURL, "file_id=file123") {
		t.Error("expected file_id parameter")
	}
	if !strings.Contains(signedURL, "client_id=test-client-id") {
		t.Error("expected client_id parameter")
	}
	if strings.Contains(signedURL, "expert_mode") {
		t.Error("did not expect expert_mode when autograph is false")
	}
	if !strings.Contains(signedURL, "signature=") {
		t.Error("expected signature parameter")
	}
}

func TestSignURLWithExpertMode(t *testing.T) {
	signer := &Signer{
		ClientID:     "test-client-id",
		ClientSecret: base64.StdEncoding.EncodeToString([]byte("test-secret-key")),
	}

	signedURL, err := signer.SignURL("file123", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(signedURL, "expert_mode=true") {
		t.Error("expected expert_mode=true when autograph is true")
	}
}

func TestSignURLWithURLSafeBase64Secret(t *testing.T) {
	// URL-safe base64: replaces + with - and / with _, omits padding.
	raw := []byte("my-secret-key")
	urlSafeSecret := base64.RawURLEncoding.EncodeToString(raw)

	signer := &Signer{
		ClientID:     "test-client-id",
		ClientSecret: urlSafeSecret,
	}

	signedURL, err := signer.SignURL("file123", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(signedURL, "signature=") {
		t.Error("expected signature parameter")
	}
}

func TestSignURLEmptyCredentials(t *testing.T) {
	signer := &Signer{}
	_, err := signer.SignURL("file123", false)
	if err == nil {
		t.Error("expected error for empty credentials")
	}
}

func TestSignURLSignatureValid(t *testing.T) {
	secret := []byte("my-secret")
	signer := &Signer{
		ClientID:     "client42",
		ClientSecret: base64.StdEncoding.EncodeToString(secret),
	}

	signedURL, err := signer.SignURL("abc456", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Extract the URL part before signature and the signature itself.
	parts := strings.SplitN(signedURL, "&signature=", 2)
	if len(parts) != 2 {
		t.Fatal("could not split signed URL")
	}
	urlPart := strings.TrimPrefix(parts[0], apiBaseURL)
	expectedSig := parts[1]

	key, err := base64.StdEncoding.DecodeString(signer.ClientSecret)
	if err != nil {
		t.Fatal(err)
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(urlPart))
	computedSig := fmt.Sprintf("%x", h.Sum(nil))

	if !hmac.Equal([]byte(expectedSig), []byte(computedSig)) {
		t.Error("signature mismatch")
	}
}

func TestHTTPDownloaderUsesTimeout(t *testing.T) {
	// Server that waits longer than the timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "download.txt")

	d := &HTTPDownloader{Timeout: 50 * time.Millisecond}
	err := d.Download(srv.URL, dest)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "Client.Timeout") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPDownloaderSuccessWithTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "download.txt")

	d := &HTTPDownloader{Timeout: 5 * time.Second}
	err := d.Download(srv.URL, dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("unexpected content: %s", string(data))
	}
}
