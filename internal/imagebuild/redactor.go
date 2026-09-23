package imagebuild

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
)

type secretWriter struct {
	mu      sync.Mutex
	out     io.Writer
	secrets []string
	pending string
	max     int
}

func newSecretWriter(out io.Writer, p Prepared) *secretWriter {
	values := []string{p.Registry.Password, base64.StdEncoding.EncodeToString([]byte(p.Registry.Username + ":" + p.Registry.Password))}
	for _, raw := range p.Env {
		if raw == "" {
			continue
		}
		values = append(values, raw)
		if u, e := url.Parse(raw); e == nil && u.User != nil {
			pass, _ := u.User.Password()
			values = append(values, pass, url.QueryEscape(pass), url.PathEscape(pass))
		}
	}
	w := &secretWriter{out: out, max: 1}
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" {
			continue
		}
		variants := []string{v}
		b, _ := json.Marshal(v)
		variants = append(variants, string(b[1:len(b)-1]))
		for _, v := range variants {
			if seen[v] {
				continue
			}
			seen[v] = true
			w.secrets = append(w.secrets, v)
			if len(v) > w.max {
				w.max = len(v)
			}
		}
	}
	sort.Slice(w.secrets, func(i, j int) bool { return len(w.secrets[i]) > len(w.secrets[j]) })
	return w
}
func (w *secretWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(p)
	return len(p), w.flush(false)
}
func (w *secretWriter) Close() error { w.mu.Lock(); defer w.mu.Unlock(); return w.flush(true) }
func (w *secretWriter) flush(final bool) error {
	var out strings.Builder
	for len(w.pending) > 0 && (final || len(w.pending) >= w.max) {
		matched := false
		for _, s := range w.secrets {
			if strings.HasPrefix(w.pending, s) {
				out.WriteString("[REDACTED]")
				w.pending = w.pending[len(s):]
				matched = true
				break
			}
		}
		if !matched {
			out.WriteByte(w.pending[0])
			w.pending = w.pending[1:]
		}
	}
	_, e := io.WriteString(w.out, out.String())
	return e
}
