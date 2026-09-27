package k8sintegration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type KubernetesClient struct {
	http      HTTPDoer
	base      *url.URL
	namespace string
}

func NewKubernetesClient(client HTTPDoer, baseURL, namespace string) (*KubernetesClient, error) {
	u, err := url.Parse(baseURL)
	if client == nil || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || !validName(namespace) {
		return nil, errors.New("invalid Kubernetes API connection")
	}
	return &KubernetesClient{http: client, base: u, namespace: namespace}, nil
}

func (c *KubernetesClient) path(kind, name string) (string, error) {
	var p string
	switch kind {
	case "Pod":
		p = "/api/v1/namespaces/" + c.namespace + "/pods"
	case "Service":
		p = "/api/v1/namespaces/" + c.namespace + "/services"
	case "Job":
		p = "/apis/batch/v1/namespaces/" + c.namespace + "/jobs"
	case "NetworkPolicy":
		p = "/apis/networking.k8s.io/v1/namespaces/" + c.namespace + "/networkpolicies"
	default:
		return "", errors.New("unsupported Kubernetes resource kind")
	}
	if name != "" {
		if len(name) > 63 || !dnsLabel.MatchString(name) {
			return "", errors.New("invalid Kubernetes resource name")
		}
		p += "/" + name
	}
	return p, nil
}

func (c *KubernetesClient) request(ctx context.Context, method, path string, body any, out any) (int, error) {
	u := *c.base
	pathOnly, query, _ := strings.Cut(path, "?")
	u.Path = strings.TrimRight(c.base.Path, "/") + pathOnly
	u.RawQuery = query
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("Kubernetes %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil && len(data) > 0 {
		if err = json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *KubernetesClient) Create(ctx context.Context, r Resource) error {
	if c == nil || len(r.Name) > 63 || !dnsLabel.MatchString(r.Name) {
		return errors.New("invalid integration resource")
	}
	p, err := c.path(r.Kind, "")
	if err != nil {
		return err
	}
	spec, ok := r.Body["spec"]
	if !ok {
		return errors.New("resource spec required")
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	h := sha256.Sum256(data)
	digest := hex.EncodeToString(h[:])
	metadata, ok := r.Body["metadata"].(map[string]any)
	if !ok {
		return errors.New("resource metadata required")
	}
	wantLabels, ok := metadata["labels"].(map[string]any)
	if !ok {
		return errors.New("resource labels required")
	}
	annotations := map[string]any{"swarmjet.io/contract-sha256": digest}
	metadata["annotations"] = annotations
	status, err := c.request(ctx, http.MethodPost, p, r.Body, nil)
	if status != http.StatusConflict {
		return err
	}
	objectPath, _ := c.path(r.Kind, r.Name)
	var existing struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
			Labels      map[string]string `json:"labels"`
			Name        string            `json:"name"`
		} `json:"metadata"`
	}
	if _, err = c.request(ctx, http.MethodGet, objectPath, nil, &existing); err != nil {
		return err
	}
	if existing.Metadata.Name != r.Name || existing.Metadata.Annotations["swarmjet.io/contract-sha256"] != digest || existing.Metadata.Labels["swarmjet.io/run-id"] != wantLabels["swarmjet.io/run-id"] || existing.Metadata.Labels["swarmjet.io/task-id"] != wantLabels["swarmjet.io/task-id"] || existing.Metadata.Labels["swarmjet.io/role"] != wantLabels["swarmjet.io/role"] {
		return errors.New("existing integration resource conflicts with requested contract")
	}
	return nil
}

func (c *KubernetesClient) Observe(ctx context.Context, kind, name string) (Status, error) {
	if c == nil {
		return Status{}, errors.New("Kubernetes client required")
	}
	p, err := c.path(kind, name)
	if err != nil {
		return Status{}, err
	}
	var raw struct {
		Status struct {
			Phase      string                                  `json:"phase"`
			PodIP      string                                  `json:"podIP"`
			Succeeded  int                                     `json:"succeeded"`
			Failed     int                                     `json:"failed"`
			Conditions []struct{ Type, Status, Reason string } `json:"conditions"`
		} `json:"status"`
	}
	if _, err = c.request(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return Status{}, err
	}
	result := Status{Exists: true, PodIP: raw.Status.PodIP}
	if kind == "Pod" {
		result.Failed = raw.Status.Phase == "Failed"
		for _, condition := range raw.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				result.Ready = true
			}
			if condition.Type == "Ready" && condition.Status == "False" {
				result.Reason = condition.Reason
			}
		}
	} else if kind == "Job" {
		result.Complete = raw.Status.Succeeded > 0
		result.Failed = raw.Status.Failed > 0
		for _, condition := range raw.Status.Conditions {
			if condition.Status == "True" && condition.Type == "Complete" {
				result.Complete = true
			}
			if condition.Status == "True" && condition.Type == "Failed" {
				result.Failed = true
				result.Reason = condition.Reason
			}
		}
	}
	return result, nil
}

func (c *KubernetesClient) Delete(ctx context.Context, kind, name string) error {
	if c == nil {
		return errors.New("Kubernetes client required")
	}
	p, err := c.path(kind, name)
	if err != nil {
		return err
	}
	_, err = c.request(ctx, http.MethodDelete, p, map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground"}, nil)
	return err
}

// JobLog reads bounded stdout before cleanup. The caller must persist this
// report outside the Pod before retiring task artifacts.
func (c *KubernetesClient) JobLog(ctx context.Context, jobName string) ([]byte, error) {
	if c == nil || len(jobName) > 63 || !dnsLabel.MatchString(jobName) {
		return nil, errors.New("invalid check Job")
	}
	path := "/api/v1/namespaces/" + c.namespace + "/pods?labelSelector=" + url.QueryEscape("batch.kubernetes.io/job-name="+jobName)
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if _, err := c.request(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	if len(list.Items) != 1 || len(list.Items[0].Metadata.Name) > 63 || !dnsLabel.MatchString(list.Items[0].Metadata.Name) {
		return nil, errors.New("expected one check Pod")
	}
	logPath := "/api/v1/namespaces/" + c.namespace + "/pods/" + list.Items[0].Metadata.Name + "/log?container=check&limitBytes=8388609"
	u := *c.base
	pathOnly, query, _ := strings.Cut(logPath, "?")
	u.Path = strings.TrimRight(c.base.Path, "/") + pathOnly
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Kubernetes Pod log: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 8<<20 {
		return nil, errors.New("check report exceeds size limit")
	}
	return b, nil
}

var _ Client = (*KubernetesClient)(nil)
