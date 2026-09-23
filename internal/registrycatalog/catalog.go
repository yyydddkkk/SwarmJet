package registrycatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"swarmjet/internal/registryconfig"
)

const acceptManifest = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

type Image struct {
	Digest    string   `json:"digest"`
	Reference string   `json:"reference"`
	Tags      []string `json:"tags"`
}

type Client struct {
	HTTP       *http.Client
	BaseURL    string
	Repository string
	Access     registryconfig.Access
}

type StatusError struct{ Code int }

func (e StatusError) Error() string { return fmt.Sprintf("仓库请求失败（HTTP %d）", e.Code) }

func New(config registryconfig.Config, access registryconfig.Access) Client {
	return Client{
		HTTP:    &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		BaseURL: "https://" + config.Address, Repository: config.Repository, Access: access,
	}
}

func (c Client) request(ctx context.Context, method, path string) (*http.Response, error) {
	if c.HTTP == nil {
		return nil, errors.New("registry HTTP client unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.Access.Username, c.Access.Password)
	req.Header.Set("Accept", acceptManifest)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, StatusError{Code: res.StatusCode}
	}
	return res, nil
}

func (c Client) tags(ctx context.Context) ([]string, error) {
	tags := make([]string, 0)
	last := ""
	for {
		path := "/v2/" + c.Repository + "/tags/list?n=100"
		if last != "" {
			path += "&last=" + url.QueryEscape(last)
		}
		res, err := c.request(ctx, http.MethodGet, path)
		if err != nil {
			var status StatusError
			if errors.As(err, &status) && status.Code == http.StatusNotFound && last == "" {
				return []string{}, nil
			}
			return nil, err
		}
		var page struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&page)
		res.Body.Close()
		if err != nil || page.Name != c.Repository {
			return nil, errors.New("仓库标签响应无效")
		}
		if len(tags)+len(page.Tags) > 2000 {
			return nil, errors.New("当前仓库标签超过 2000 个，请先从仓库侧整理")
		}
		if len(page.Tags) == 0 && res.Header.Get("Link") != "" {
			return nil, errors.New("仓库标签分页无效")
		}
		for _, tag := range page.Tags {
			if tag <= last || strings.ContainsAny(tag, "/\r\n") {
				return nil, errors.New("仓库标签顺序无效")
			}
			last = tag
			tags = append(tags, tag)
		}
		if res.Header.Get("Link") == "" {
			return tags, nil
		}
		if !strings.Contains(res.Header.Get("Link"), `rel="next"`) {
			return nil, errors.New("仓库标签分页链接无效")
		}
	}
}

func (c Client) List(ctx context.Context) ([]Image, error) {
	tags, err := c.tags(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		tag, digest string
		err         error
	}
	jobs := make(chan string)
	results := make(chan result, len(tags))
	workers := min(8, len(tags))
	for i := 0; i < workers; i++ {
		go func() {
			for tag := range jobs {
				res, err := c.request(ctx, http.MethodHead, "/v2/"+c.Repository+"/manifests/"+url.PathEscape(tag))
				if err != nil {
					results <- result{tag: tag, err: err}
					continue
				}
				res.Body.Close()
				results <- result{tag: tag, digest: res.Header.Get("Docker-Content-Digest")}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, tag := range tags {
			select {
			case jobs <- tag:
			case <-ctx.Done():
				return
			}
		}
	}()
	byDigest := map[string]*Image{}
	for range tags {
		var resolved result
		select {
		case resolved = <-results:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if resolved.err != nil {
			return nil, resolved.err
		}
		if !validDigest(resolved.digest) {
			return nil, errors.New("仓库返回无效的镜像摘要")
		}
		item := byDigest[resolved.digest]
		if item == nil {
			item = &Image{Digest: resolved.digest, Reference: c.Access.Address + "/" + c.Repository + "@" + resolved.digest, Tags: []string{}}
			byDigest[resolved.digest] = item
		}
		item.Tags = append(item.Tags, resolved.tag)
	}
	images := make([]Image, 0, len(byDigest))
	for _, item := range byDigest {
		sort.Strings(item.Tags)
		images = append(images, *item)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Tags[0] > images[j].Tags[0] })
	return images, nil
}

func validDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return false
	}
	for _, ch := range digest[7:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func (c Client) Delete(ctx context.Context, digest string) error {
	if !validDigest(digest) {
		return errors.New("镜像摘要无效")
	}
	res, err := c.request(ctx, http.MethodDelete, "/v2/"+c.Repository+"/manifests/"+digest)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}
