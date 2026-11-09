package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// RequestIDHeader lines up artifactweave's logs with a reverse proxy's. It is
// artifactweave's choice, not Artifactory's: Artifactory's correlation header
// is X-Artifactory-Trace-Id, sent only when artifactory.trace.id.response.header
// is true, from 7.79 (JFrog KB "How to correlate Artifactory and NGINX logs").
const RequestIDHeader = "X-Request-ID"

// RequestID reuses a caller-supplied id so a client can correlate retries, and
// mints one otherwise. It echoes the id in RequestIDHeader and keeps it on the
// request context (RequestIDFrom).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is a broken host; a timestamp keeps logs usable
		// rather than failing every request.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// AccessLog writes one slog line per request. It wraps the writer so the
// status and byte count are known after the handler returns. A request whose
// handler noted a cause (NoteCause) logs at ERROR with it, and one that
// authenticated names its user (NoteUser). The path is logged as redact
// returns it, so a credential that a path names never reaches the log.
func AccessLog(
	logger *slog.Logger,
	redact func(path string) string,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			var cause error
			var user string
			next.ServeHTTP(ww, r.WithContext(WithUser(WithCause(r.Context(), &cause), &user)))
			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", redact(r.URL.Path)),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFrom(r.Context())),
			}
			if user != "" {
				attrs = append(attrs, slog.String("user", user))
			}
			level := slog.LevelInfo
			if cause != nil {
				level = slog.LevelError
				attrs = append(attrs, slog.String("error", cause.Error()))
			}
			logger.LogAttrs(r.Context(), level, "request", attrs...)
		})
	}
}

// RecoverPanic turns a panic into a 500 using the request's error response
// profile, so a bug in one handler never leaks a Go stack trace to a client.
// The panic's log line names the path as redact returns it, as AccessLog's
// does.
func RecoverPanic(
	logger *slog.Logger,
	redact func(path string) string,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			defer func() {
				if rec := recover(); rec != nil {
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}
					logger.LogAttrs(ctx, slog.LevelError, "panic",
						slog.Any("panic", rec),
						slog.String("path", redact(r.URL.Path)),
						slog.String("request_id", RequestIDFrom(ctx)),
					)
					apierror.Write(
						w,
						ErrorProfile(r),
						apierror.New(http.StatusInternalServerError, "internal error"),
					)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit rejects management request bodies above limit. It is mounted only
// on JSON management groups; artifact uploads are streamed and must never pass
// through it. A declared Content-Length above the limit is refused up front;
// an undeclared body is capped by MaxBytesReader so a strict decoder sees an
// error instead of an unbounded read.
func BodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				apierror.Write(w, ErrorProfile(r), BodyTooLarge(limit))
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyTooLarge is BodyLimit's 413. A handler that meets the MaxBytesReader
// BodyLimit installs answers with it too, so a body over the limit gets the
// same answer whether its length was declared or not.
func BodyTooLarge(limit int64) *apierror.Error {
	return apierror.New(
		http.StatusRequestEntityTooLarge,
		"request body exceeds "+strconv.FormatInt(limit, 10)+" bytes",
	)
}

// NormalizeContentType maps the vendor media types Artifactory clients send,
// such as application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json,
// onto application/json so the generated strict decoders accept them. The
// original value is kept in a context value (OriginalContentType) for
// handlers that care about the exact type. application/yaml is left alone;
// the descriptor PATCH handler is hand-written and reads it directly. A
// request keep accepts retains its media type, for protocols whose vendor
// types are part of the exchange or of the content stored and served back.
func NormalizeContentType(keep func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ct := r.Header.Get("Content-Type")
			if ct != "" && !keep(r) {
				if mt, params, err := mime.ParseMediaType(
					ct,
				); err == nil &&
					strings.HasSuffix(mt, "+json") {
					// ParseMediaType lowercases the type; keep the client's spelling
					// (Artifactory vendor types use CamelCase) for handlers that echo it.
					original := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
					r = r.WithContext(WithOriginalContentType(r.Context(), original))
					r.Header.Set("Content-Type", mime.FormatMediaType("application/json", params))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BindingError renders request binding and response rendering failures from
// the generated strict handlers in the right envelope. A MaxBytesError from
// BodyLimit becomes 413 rather than the generic 400.
func BindingError(status int) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierror.Write(
				w,
				ErrorProfile(r),
				apierror.New(http.StatusRequestEntityTooLarge, err.Error()),
			)
			return
		}
		// A typed error carries its own status (501 from the generated stubs,
		// 4xx from validation); only untyped errors take the binder's default.
		var typed *apierror.Error
		if errors.As(err, &typed) {
			apierror.Write(w, ErrorProfile(r), typed)
			return
		}
		if status >= http.StatusInternalServerError {
			NoteCause(r.Context(), err)
		}
		apierror.Write(w, ErrorProfile(r), apierror.New(status, err.Error()))
	}
}

// CollectionSlash routes each path that is a key of m as the value m gives
// it, for the collection paths clients call with a trailing slash, which chi
// would otherwise read as a member with an empty key. Only the exact paths
// of m are rewritten, because a trailing slash elsewhere can name a folder.
func CollectionSlash(m map[string]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bare, ok := m[r.URL.Path]; ok {
				r.URL.Path, r.URL.RawPath = bare, ""
				if rctx := chi.RouteContext(r.Context()); rctx != nil {
					rctx.RoutePath = r.URL.Path
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
