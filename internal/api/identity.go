package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"swarmjet/internal/store"
)

// SMTPConfig requires STARTTLS and certificate validation. PublicURL is the
// browser origin used in email links, normally the console's external URL.
type SMTPConfig struct {
	Addr      string
	Username  string
	Password  string
	From      string
	PublicURL string
}

const passwordMemory uint32 = 64 * 1024
const passwordIterations uint32 = 3
const passwordThreads uint8 = 2
const passwordKeyLen uint32 = 32

func hashAccountPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordThreads, passwordKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", passwordMemory, passwordIterations, passwordThreads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyAccountPassword(encoded, password string) bool {
	var memory, iterations uint32
	var threads uint8
	// Sscanf's %s consumes delimiters. Split explicitly, and bound attacker
	// controlled parameters before allocating memory.
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	if memory != passwordMemory || iterations != passwordIterations || threads != passwordThreads {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != int(passwordKeyLen) {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, passwordKeyLen)
	return subtle.ConstantTimeCompare(got, expected) == 1
}

func normalizeAccountEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid email")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || parsed.Name != "" {
		return "", errors.New("invalid email")
	}
	return value, nil
}

func validAccountPassword(password string) bool { return len(password) >= 12 && len(password) <= 1024 }

func (a *server) sendAccountEmail(ctx context.Context, to, subject, body string) error {
	cfg := a.cfg.SMTP
	if cfg.Addr == "" || cfg.From == "" || cfg.PublicURL == "" {
		return errors.New("SMTP not configured")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil || from.Address == "" {
		return errors.New("invalid SMTP sender")
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		conn.Close()
		return err
	}
	if err = conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		conn.Close()
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()
	ok, _ := client.Extension("STARTTLS")
	if !ok {
		return errors.New("SMTP STARTTLS required")
	}
	if err = client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, host)); err != nil {
			return err
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return err
	}
	if err = client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := "From: " + from.String() + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body + "\r\n"
	_, err = io.WriteString(writer, message)
	if err != nil {
		writer.Close()
		return err
	}
	return writer.Close()
}

func (a *server) identityPublic(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/api/register":
		if r.Method != http.MethodPost {
			problem(w, 405, "method not allowed")
			return true
		}
		a.registerAccount(w, r)
		return true
	case "/api/register/verify":
		if r.Method != http.MethodPost {
			problem(w, 405, "method not allowed")
			return true
		}
		var req struct {
			Token       string `json:"token"`
			DisplayName string `json:"displayName"`
			Password    string `json:"password"`
		}
		if !decode(w, r, &req) {
			return true
		}
		name := strings.TrimSpace(req.DisplayName)
		if len(req.Token) != 64 || len(name) < 1 || len(name) > 100 || strings.ContainsAny(name, "\r\n") || !validAccountPassword(req.Password) {
			problem(w, 400, "invalid verification details")
			return true
		}
		passwordHash, err := hashAccountPassword(req.Password)
		if err != nil {
			failure(w, err)
			return true
		}
		if err := a.s.VerifyEmailToken(r.Context(), tokenHash(req.Token), passwordHash, name); err != nil {
			problem(w, 400, "invalid or expired token")
			return true
		}
		w.WriteHeader(204)
		return true
	case "/api/password-resets":
		if r.Method != http.MethodPost {
			problem(w, 405, "method not allowed")
			return true
		}
		a.requestPasswordReset(w, r)
		return true
	case "/api/password-resets/confirm":
		if r.Method != http.MethodPost {
			problem(w, 405, "method not allowed")
			return true
		}
		var req struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if !decode(w, r, &req) {
			return true
		}
		if len(req.Token) != 64 || !validAccountPassword(req.Password) {
			problem(w, 400, "invalid token or password")
			return true
		}
		hash, err := hashAccountPassword(req.Password)
		if err != nil {
			failure(w, err)
			return true
		}
		if err = a.s.ResetPasswordWithToken(r.Context(), tokenHash(req.Token), hash); err != nil {
			problem(w, 400, "invalid or expired token")
			return true
		}
		w.WriteHeader(204)
		return true
	}
	return false
}

func (a *server) registerAccount(w http.ResponseWriter, r *http.Request) {
	if !a.loginAllowed(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "too many account requests")
		return
	}
	if a.s == nil || a.cfg.SMTP.Addr == "" || a.cfg.SMTP.PublicURL == "" {
		problem(w, 503, "account registration unavailable")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	email, err := normalizeAccountEmail(req.Email)
	if err != nil {
		problem(w, 400, "invalid account details")
		return
	}
	id, err := newID()
	if err != nil {
		failure(w, err)
		return
	}
	u := store.User{ID: id, Email: email, DisplayName: "", PasswordHash: ""}
	newUser := true
	if err = a.s.RegisterUser(r.Context(), u); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			failure(w, err)
			return
		}
		existing, lookupErr := a.s.UserByEmail(r.Context(), email)
		if lookupErr != nil || existing.Status != "pending_email" {
			respond(w, 202, map[string]string{"status": "pending_email"})
			return
		}
		id = existing.ID
		newUser = false
	}
	token, err := opaqueToken()
	if err != nil {
		if newUser {
			_ = a.s.DeleteUnverifiedUser(r.Context(), id)
		}
		failure(w, err)
		return
	}
	if err = a.s.IssueEmailToken(r.Context(), id, "verify", tokenHash(token), time.Now().Add(24*time.Hour)); err != nil {
		if errors.Is(err, store.ErrEmailCooldown) {
			respond(w, 202, map[string]string{"status": "pending_email"})
			return
		}
		if newUser {
			_ = a.s.DeleteUnverifiedUser(r.Context(), id)
		}
		failure(w, err)
		return
	}
	link := strings.TrimRight(a.cfg.SMTP.PublicURL, "/") + "/#/verify?token=" + url.QueryEscape(token)
	if err = a.sendAccountEmail(r.Context(), email, "Verify your SwarmJet account", "Open this link to verify your email address:\n"+link+"\n\nThis link expires in 24 hours."); err != nil {
		if newUser {
			_ = a.s.DeleteUnverifiedUser(r.Context(), id)
		}
		problem(w, 503, "verification email unavailable")
		return
	}
	respond(w, 202, map[string]string{"status": "pending_email"})
}

func (a *server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !a.loginAllowed(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "too many account requests")
		return
	}
	if a.s == nil || a.cfg.SMTP.Addr == "" || a.cfg.SMTP.PublicURL == "" {
		problem(w, 503, "password reset unavailable")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &req) {
		return
	}
	email, err := normalizeAccountEmail(req.Email)
	if err != nil {
		problem(w, 400, "invalid email")
		return
	}
	// The response does not reveal whether the address is registered.
	defer respond(w, 202, map[string]string{"status": "accepted"})
	u, err := a.s.UserByEmail(r.Context(), email)
	if err != nil || (u.Status != "active" && u.Status != "pending_approval") {
		return
	}
	token, err := opaqueToken()
	if err != nil {
		return
	}
	if err = a.s.IssueEmailToken(r.Context(), u.ID, "reset", tokenHash(token), time.Now().Add(time.Hour)); err != nil {
		return
	}
	link := strings.TrimRight(a.cfg.SMTP.PublicURL, "/") + "/#/reset-password?token=" + url.QueryEscape(token)
	_ = a.sendAccountEmail(r.Context(), email, "Reset your SwarmJet password", "Open this link to reset your password:\n"+link+"\n\nThis link expires in 1 hour.")
}

func (a *server) identityPrivate(w http.ResponseWriter, r *http.Request, p store.Principal) bool {
	if r.URL.Path != "/api/users" && !strings.HasPrefix(r.URL.Path, "/api/users/") {
		return false
	}
	if r.URL.Path == "/api/users" {
		if r.Method != http.MethodGet {
			problem(w, 405, "method not allowed")
			return true
		}
		status := r.URL.Query().Get("status")
		if status == "pending" {
			status = "pending_approval"
		}
		switch status {
		case "", "pending_email", "pending_approval", "active", "disabled", "rejected":
		default:
			problem(w, 400, "invalid status")
			return true
		}
		if !p.PlatformAdmin {
			if status != "" && status != "active" {
				problem(w, 403, "platform administrator required")
				return true
			}
			roles, err := a.s.VisibleProjectIDs(r.Context(), p.UserID)
			if err != nil {
				failure(w, err)
				return true
			}
			owner := false
			for _, role := range roles {
				if role == store.RoleOwner {
					owner = true
					break
				}
			}
			if !owner {
				problem(w, 403, "project owner required")
				return true
			}
			status = "active"
		}
		users, err := a.s.ListUsers(r.Context(), status)
		if err != nil {
			failure(w, err)
			return true
		}
		respond(w, 200, map[string]any{"users": users})
		return true
	}
	if !p.PlatformAdmin {
		problem(w, 403, "platform administrator required")
		return true
	}
	if r.Method != http.MethodPost {
		problem(w, 405, "method not allowed")
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/users/"), "/")
	if len(parts) != 2 || !validAccountID(parts[0]) {
		problem(w, 404, "not found")
		return true
	}
	if p.UserID == parts[0] {
		problem(w, 403, "cannot change own account status")
		return true
	}
	if parts[1] == "platform-admin" {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if !decode(w, r, &req) {
			return true
		}
		if err := a.s.SetPlatformAdmin(r.Context(), parts[0], req.Enabled, p.UserID); err != nil {
			failure(w, err)
			return true
		}
		w.WriteHeader(204)
		return true
	}
	var status string
	switch parts[1] {
	case "approve":
		status = "active"
	case "reject":
		status = "rejected"
	case "disable":
		status = "disabled"
	default:
		problem(w, 404, "not found")
		return true
	}
	if err := a.s.SetUserStatus(r.Context(), parts[0], status, p.UserID); err != nil {
		failure(w, err)
		return true
	}
	w.WriteHeader(204)
	return true
}

func validAccountID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// loginAccount only handles a successfully authenticated local member.
// The caller retains control of the breakglass credential path.
func (a *server) loginAccount(w http.ResponseWriter, r *http.Request, email, password string) bool {
	if a.s == nil {
		return false
	}
	normalized, err := normalizeAccountEmail(email)
	if err != nil {
		return false
	}
	u, err := a.s.UserByEmail(r.Context(), normalized)
	if err != nil || !verifyAccountPassword(u.PasswordHash, password) || u.Status != "active" {
		return false
	}
	token, err := opaqueToken()
	if err != nil {
		failure(w, err)
		return true
	}
	if err = a.s.AddUserSession(r.Context(), tokenHash(token), u.ID); err != nil {
		failure(w, err)
		return true
	}
	a.cookie(w, token, 12*60*60)
	respond(w, 200, map[string]any{"username": u.Email, "userId": u.ID, "displayName": u.DisplayName, "platformAdmin": u.PlatformAdmin})
	_ = a.s.Audit(r.Context(), store.Principal{UserID: u.ID}, "session.login", "user", u.ID, map[string]any{"ip": r.RemoteAddr})
	return true
}
