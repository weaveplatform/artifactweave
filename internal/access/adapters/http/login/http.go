package login

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
)

const (
	pagePath      = "/ui/native-login"
	authorizePath = "/ui/terraform/oauth2/authorize"
	tokenPath     = "/artifactory/api/oauth2/token"
	cookieName    = "aw_native_session"
)

// Handler serves only the optional native-client approval surface. Origin is
// validated operator configuration; no request header is used to build URLs.
type Handler struct {
	Service accessapi.NativeLogin
	Origin  string
}

// Enabled rejects an incomplete embedding as well as unsafe canonical origins.
func (h Handler) Enabled() bool { return h.Service != nil && accessapi.ValidNativeOrigin(h.Origin) }

// Public reports only the exact enabled bootstrap routes. In particular it
// does not exempt a repository's ordinary reads, writes or legacy login.
func (h Handler) Public(r *http.Request) bool {
	if !h.Enabled() {
		return false
	}
	p := r.URL.Path
	if p == pagePath {
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	if p == authorizePath {
		return r.Method == http.MethodGet
	}
	if p == tokenPath {
		return r.Method == http.MethodPost
	}
	_, start, done := npmPath(p)
	return start && r.Method == http.MethodPost || done && r.Method == http.MethodGet
}

// Mount adds routes only when enabled, preserving the existing disabled npm
// routing and Terraform pasted-token fallback.
func (h Handler) Mount(r chi.Router) {
	if !h.Enabled() {
		return
	}
	r.Get(pagePath, h.page)
	r.Post(pagePath, h.form)
	r.Get(authorizePath, h.authorize)
	r.Post(tokenPath, h.exchange)
	r.Post("/artifactory/api/npm/{repoKey}/-/v1/login", h.start)
	r.Get("/artifactory/api/npm/{repoKey}/-/v1/done", h.poll)
}

func npmPath(path string) (string, bool, bool) {
	rest, ok := strings.CutPrefix(path, "/artifactory/api/npm/")
	if !ok {
		return "", false, false
	}
	key, tail, ok := strings.Cut(rest, "/")
	if !ok || key == "" {
		return "", false, false
	}
	return key, tail == "-/v1/login", tail == "-/v1/done"
}

func secureHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().
		Set("Content-Security-Policy", "default-src 'none'; form-action 'self' http://localhost:10000 http://localhost:10001 http://localhost:10002 http://localhost:10003 http://localhost:10004 http://localhost:10005 http://localhost:10006 http://localhost:10007 http://localhost:10008 http://localhost:10009 http://localhost:10010; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
}

func answer(w http.ResponseWriter, status int, v any) {
	secureHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func failed(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, accessapi.ErrNativeCapacity) {
		status = http.StatusTooManyRequests
	}
	answer(w, status, map[string]string{"error": "approval_unavailable"})
}

func (h Handler) start(w http.ResponseWriter, r *http.Request) {
	// npm sends an empty JSON object. Bound and validate it rather than retaining
	// arbitrary fields in an unauthenticated session.
	var body map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&body); err != nil || body == nil || len(body) != 0 {
		failed(w, accessapi.ErrNativeLogin)
		return
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		failed(w, accessapi.ErrNativeLogin)
		return
	}
	key := chi.URLParam(r, "repoKey")
	s, err := h.Service.Start(r.Context(), accessapi.NativeRequest{Kind: "npm", Repository: key})
	if err != nil {
		failed(w, err)
		return
	}
	answer(
		w,
		http.StatusOK,
		map[string]string{
			"loginUrl": h.Origin + pagePath + "?request=" + url.QueryEscape(s.Browser),
			"doneUrl":  h.Origin + "/artifactory/api/npm/" + url.PathEscape(key) + "/-/v1/done?sessionId=" + url.QueryEscape(s.Poll),
		},
	)
}

func (h Handler) poll(w http.ResponseWriter, r *http.Request) {
	t, err := h.Service.Poll(r.Context(), r.URL.Query().Get("sessionId"), chi.URLParam(r, "repoKey"))
	if errors.Is(err, accessapi.ErrNativePending) {
		w.Header().Set("Retry-After", "5")
		answer(w, http.StatusAccepted, map[string]string{})
		return
	}
	if err != nil {
		failed(w, err)
		return
	}
	answer(w, http.StatusOK, map[string]string{"token": t.AccessToken})
}

func (h Handler) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" {
		failed(w, accessapi.ErrNativeLogin)
		return
	}
	s, err := h.Service.Start(
		r.Context(),
		accessapi.NativeRequest{
			Kind:            "terraform",
			ClientID:        q.Get("client_id"),
			RedirectURI:     q.Get("redirect_uri"),
			State:           q.Get("state"),
			Challenge:       q.Get("code_challenge"),
			ChallengeMethod: q.Get("code_challenge_method"),
		},
	)
	if err != nil {
		failed(w, err)
		return
	}
	secureHeaders(w)
	http.Redirect(w, r, h.Origin+pagePath+"?request="+url.QueryEscape(s.Browser), http.StatusSeeOther)
}

func (h Handler) exchange(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.ParseForm() != nil || r.PostForm.Get("grant_type") != "authorization_code" ||
		r.PostForm.Has("client_secret") {
		answer(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	t, err := h.Service.Exchange(
		r.Context(),
		accessapi.NativeExchange{
			Code:        r.PostForm.Get("code"),
			ClientID:    r.PostForm.Get("client_id"),
			RedirectURI: r.PostForm.Get("redirect_uri"),
			Verifier:    r.PostForm.Get("code_verifier"),
		},
	)
	if err != nil {
		answer(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	answer(w, http.StatusOK, map[string]any{"access_token": t.AccessToken, "token_type": "Bearer", "expires_in": t.ExpiresIn})
}

func browserCookie(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (h Handler) setCookie(w http.ResponseWriter, value string) {
	http.SetCookie(
		w,
		&http.Cookie{ //nolint:gosec // G124: Enabled permits HTTP only on loopback; all HTTPS origins set Secure.
			Name:     cookieName,
			Value:    value,
			Path:     pagePath,
			HttpOnly: true,
			Secure:   strings.HasPrefix(h.Origin, "https://"),
			SameSite: http.SameSiteStrictMode,
			MaxAge:   300,
		},
	)
}

func (h Handler) page(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	p, err := h.Service.Open(r.URL.Query().Get("request"), browserCookie(r))
	if err != nil {
		failed(w, err)
		return
	}
	h.render(w, p)
}

func (h Handler) render(w http.ResponseWriter, p accessapi.NativePage) {
	h.setCookie(w, p.Session)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = approvalTemplate.Execute(w, p)
}

func (h Handler) form(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w)
	// CSRF protects capability binding; exact Origin additionally prevents
	// cross-origin login and consent submissions, including login CSRF.
	if r.Header.Get("Origin") != h.Origin {
		failed(w, accessapi.ErrNativeLogin)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		failed(w, accessapi.ErrNativeLogin)
		return
	}
	b, c, csrf := r.URL.Query().Get("request"), browserCookie(r), r.PostForm.Get("csrf")
	switch r.PostForm.Get("action") {
	case "login":
		p, err := h.Service.Login(r.Context(), b, c, csrf, r.PostForm.Get("username"), r.PostForm.Get("password"))
		if err != nil {
			failed(w, err)
			return
		}
		h.render(w, p)
	case "approve", "deny":
		redirect, err := h.Service.Consent(r.Context(), b, c, csrf, r.PostForm.Get("action") == "approve")
		if err != nil {
			failed(w, err)
			return
		}
		if redirect != "" {
			http.Redirect(w, r, redirect, http.StatusSeeOther) //nolint:gosec // G710: access binds this to its exact validated localhost callback; code/state are query-escaped.
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Approval complete</title><p>Return to your client. If you denied access, cancel the waiting login.</p>"))
	case "logout":
		if err := h.Service.Logout(b, c, csrf); err != nil {
			failed(w, err)
			return
		}
		h.setCookie(w, "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Signed out</title><p>This approval was cancelled.</p>"))
	default:
		failed(w, accessapi.ErrNativeLogin)
	}
}

var approvalTemplate = template.Must(
	template.New("approval").
		Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Approve native login</title><h1>Approve native login</h1><p>Client: {{.Client}}{{if .Repository}}. Repository: {{.Repository}}{{end}}</p>{{if .Approved}}<p>Approval granted. Return to your client.</p>{{else}}<form method="post"><input type="hidden" name="csrf" value="{{.CSRF}}">{{if .User}}<p>Signed in as {{.User}}. Approve a client access token with your current permissions?</p><button name="action" value="approve">Approve</button><button name="action" value="deny">Deny</button><button name="action" value="logout">Sign out and cancel</button>{{else}}<label>Username <input name="username" autocomplete="username" required maxlength="255"></label><label>Password <input name="password" type="password" autocomplete="current-password" required maxlength="4096"></label><button name="action" value="login">Sign in</button>{{end}}</form>{{end}}</html>`),
)
