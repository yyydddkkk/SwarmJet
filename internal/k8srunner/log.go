package k8srunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxPodLogBytes = 256 << 10

// ReadPodLog returns at most 256 KiB of one attempt's retained Pod log. The
// platform must validate the JSON result and lease before persisting it.
func (c *Controller) ReadPodLog(ctx context.Context, executionID string, generation int64) ([]byte, error) {
	pods, err := c.ObservePods(ctx, executionID, generation)
	if err != nil {
		return nil, err
	}
	if len(pods) != 1 {
		return nil, fmt.Errorf("expected one Pod for attempt; found %d", len(pods))
	}
	if !validDNS(pods[0].Name) || pods[0].UID == "" {
		return nil, errors.New("invalid Pod identity")
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/api/v1/namespaces/" + c.namespace + "/pods/" + pods[0].Name + "/log"
	query := url.Values{}
	query.Set("container", "runner")
	query.Set("limitBytes", fmt.Sprint(maxPodLogBytes))
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPodLogBytes+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Kubernetes Pod log HTTP %d: %s", resp.StatusCode, string(data))
	}
	if len(data) > maxPodLogBytes {
		return nil, errors.New("Pod log exceeds limit")
	}
	return data, nil
}
