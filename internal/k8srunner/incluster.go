package k8srunner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header = clone.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// NewInClusterController uses the trusted control plane's ServiceAccount.
// The runner ServiceAccount has no token or Kubernetes API permissions.
func NewInClusterController(namespace string, fence Fence) (*Controller, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("Kubernetes service address is unavailable")
	}
	token, err := os.ReadFile(serviceAccountDir + "/token")
	if err != nil {
		return nil, err
	}
	cert, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		return nil, errors.New("invalid Kubernetes CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ResponseHeaderTimeout: 20 * time.Second}
	client := &http.Client{Transport: bearerTransport{base: transport, token: strings.TrimSpace(string(token))}, Timeout: 30 * time.Second}
	return NewController(client, fmt.Sprintf("https://%s:%s", host, port), namespace, fence)
}

// StaticFence is useful only for isolated deterministic tests.
func StaticFence(allowed bool) Fence {
	return func(context.Context, string, int64) (bool, error) { return allowed, nil }
}
