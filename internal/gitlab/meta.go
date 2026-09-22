package gitlab

import (
	"context"
	"net/url"
	"strconv"
)

type Namespace struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullPath string `json:"full_path"`
	Kind     string `json:"kind"`
}

func (c *Client) Namespaces(ctx context.Context, search string, page int) ([]Namespace, bool, error) {
	if page < 1 {
		page = 1
	}
	q := url.Values{"search": {search}, "page": {strconv.Itoa(page)}, "per_page": {"50"}}
	var rows []Namespace
	h, err := c.request(ctx, "GET", "/namespaces?"+q.Encode(), nil, &rows)
	if err != nil {
		return nil, false, err
	}
	if rows == nil {
		rows = []Namespace{}
	}
	return rows, h.Get("X-Next-Page") != "", nil
}
func (c *Client) Namespace(ctx context.Context, id int64) (Namespace, error) {
	var n Namespace
	_, err := c.request(ctx, "GET", "/namespaces/"+strconv.FormatInt(id, 10), nil, &n)
	return n, err
}

type MetaProject struct {
	ProjectInfo
	Empty      bool   `json:"empty_repo"`
	Visibility string `json:"visibility"`
}

func (c *Client) MetaProject(ctx context.Context, idOrPath string) (MetaProject, error) {
	var p MetaProject
	_, err := c.request(ctx, "GET", "/projects/"+url.PathEscape(idOrPath), nil, &p)
	return p, err
}
func (c *Client) CreateMetaProject(ctx context.Context, namespace int64, path, description string) (MetaProject, error) {
	var p MetaProject
	_, err := c.request(ctx, "POST", "/projects", map[string]any{"namespace_id": namespace, "name": path, "path": path, "description": description, "visibility": "private", "initialize_with_readme": false, "auto_devops_enabled": false}, &p)
	return p, err
}
