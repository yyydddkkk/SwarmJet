package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"

	"swarmjet/internal/observability"
)

var ErrUnsupportedUploadImage = errors.New("GitLab upload is not a supported image")

// DownloadUploadImage retrieves a Markdown upload through GitLab's project API.
// The browser never receives the personal token or an upstream redirect.
func (c *Client) DownloadUploadImage(ctx context.Context, projectID int64, secret, filename string) ([]byte, string, error) {
	path := fmt.Sprintf("/projects/%d/uploads/%s/%s", projectID, secret, url.PathEscape(filename))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req, span := observability.TraceOutboundHTTP(req, "gitlab")
	defer span.End()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", &APIError{StatusCode: resp.StatusCode, Reason: gitlabErrorReason(resp.Body, c.token)}
	}
	const maxImage = 20 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImage+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxImage {
		return nil, "", errors.New("GitLab image exceeds 20 MiB")
	}
	if len(data) == 0 {
		return nil, "", ErrUnsupportedUploadImage
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	sniffed, _, _ := mime.ParseMediaType(http.DetectContentType(data))
	switch sniffed {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return data, sniffed, nil
	case "application/octet-stream":
		if mediaType == "image/avif" {
			return data, mediaType, nil
		}
	}
	return nil, "", ErrUnsupportedUploadImage
}
