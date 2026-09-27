package k8sintegration

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = copy.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}

// NewInClusterClient uses the trusted controller ServiceAccount. Test and
// service Pods disable token automount and must not use this constructor.
func NewInClusterClient(namespace string) (*KubernetesClient, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("Kubernetes API address unavailable")
	}
	base := "/var/run/secrets/kubernetes.io/serviceaccount"
	token, err := os.ReadFile(base + "/token")
	if err != nil {
		return nil, err
	}
	cert, err := os.ReadFile(base + "/ca.crt")
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		return nil, errors.New("invalid Kubernetes CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ResponseHeaderTimeout: 20 * time.Second}
	client := &http.Client{Transport: bearerTransport{base: transport, token: strings.TrimSpace(string(token))}, Timeout: 30 * time.Second}
	return NewKubernetesClient(client, fmt.Sprintf("https://%s:%s", host, port), namespace)
}
