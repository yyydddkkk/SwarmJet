package githubapp

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInstallationTokenScopeAndJWT(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	app, e := New(Config{AppID: 42, PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})})
	if e != nil {
		t.Fatal(e)
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			t.Fatal("invalid JWT")
		}
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		if e := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); e != nil {
			t.Fatal(e)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct {
			Issuer  string `json:"iss"`
			Expires int64  `json:"exp"`
		}
		json.Unmarshal(raw, &claims)
		if claims.Issuer != "42" || claims.Expires <= time.Now().Unix() {
			t.Fatal("bad claims")
		}
		var body struct {
			IDs []int64 `json:"repository_ids"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.IDs) != 2 || body.IDs[0] != 7 || body.IDs[1] != 9 {
			t.Fatalf("scope=%+v", body)
		}
		fmt.Fprintf(w, `{"token":"short-lived","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	}))
	defer s.Close()
	app.baseURL = s.URL
	app.http = s.Client()
	token, e := app.InstallationToken(context.Background(), 6, []int64{7, 9})
	if e != nil || token.Token != "short-lived" {
		t.Fatalf("%+v %v", token, e)
	}
	if _, e = app.InstallationToken(context.Background(), 6, nil); e == nil {
		t.Fatal("unscoped token accepted")
	}
}
func TestWebhookSignature(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifyWebhook("secret", body, signature) || VerifyWebhook("other", body, signature) || VerifyWebhook("", body, signature) {
		t.Fatal("signature verification incorrect")
	}
}

func TestDependencyTokenIsReadOnlyAndInstallationBound(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(Config{AppID: 42, PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})})
	if err != nil {
		t.Fatal(err)
	}
	mintCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `{"id":6,"account":{"login":"owner"}}`)
			return
		}
		mintCount++
		var body struct {
			IDs         []int64           `json:"repository_ids"`
			Names       []string          `json:"repositories"`
			Permissions map[string]string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Permissions) != 2 || body.Permissions["issues"] != "read" || body.Permissions["metadata"] != "read" {
			t.Fatalf("permissions: %+v", body.Permissions)
		}
		if mintCount == 1 && (len(body.Names) != 1 || body.Names[0] != "external") {
			t.Fatalf("name scope: %+v", body)
		}
		if mintCount == 2 && (len(body.IDs) != 1 || body.IDs[0] != 9) {
			t.Fatalf("ID scope: %+v", body)
		}
		fmt.Fprintf(w, `{"token":"read-only","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
	}))
	defer server.Close()
	app.baseURL = server.URL
	app.http = server.Client()
	if _, err = app.ReadOnlyRepositoryToken(context.Background(), 6, "owner/external"); err != nil {
		t.Fatal(err)
	}
	if _, err = app.ReadOnlyInstallationToken(context.Background(), 6, []int64{9}); err != nil {
		t.Fatal(err)
	}
	if _, err = app.ReadOnlyRepositoryToken(context.Background(), 6, "other/external"); err == nil {
		t.Fatal("cross-installation name accepted")
	}
	if mintCount != 2 {
		t.Fatalf("unexpected mint count %d", mintCount)
	}
}
