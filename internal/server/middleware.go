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

	"github.com/go-chi/chi/v5/middleware"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
)

// requestIDHeader lines up artifactweave's logs with a reverse proxy's. It is
// artifactweave's choice, not Artifactory's: Artifactory's correlation header
// is X-Artifactory-Trace-Id, sent only when artifactory.trace.id.response.header
// is true, from 7.79 (JFrog KB "How to correlate Artifactory and NGINX logs").
const requestIDHeader = "X-Request-ID"

// requestID reuses a caller-supplied id so a client can correlate retries, and
// mints one otherwise.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(withRequestID(r.Context(), id)))
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

// accessLog writes one slog line per request. It wraps the writer so the
// status and byte count are known after the handler returns. A request whose
// handler noted a cause (noteCause) logs at ERROR with it, and one that
// authenticated names its user (noteUser).
func accessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			var cause error
			var user string
			next.ServeHTTP(ww, r.WithContext(withUser(withCause(r.Context(), &cause), &user)))
			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", loggedPath(r.URL.Path)),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", requestIDFrom(r.Context())),
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

// recoverPanic turns a panic into a 500 in the envelope of the surface being
// served, so a bug in one handler never leaks a Go stack trace to a client.
func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
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
						slog.String("path", loggedPath(r.URL.Path)),
						slog.String("request_id", requestIDFrom(ctx)),
					)
					apierror.Write(
						w,
						surfaceFor(r.URL.Path),
						apierror.New(http.StatusInternalServerError, "internal error"),
					)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// bodyLimit rejects management request bodies above limit. It is mounted only
// on JSON management groups; artifact uploads are streamed and must never pass
// through it. A declared Content-Length above the limit is refused up front;
// an undeclared body is capped by MaxBytesReader so a strict decoder sees an
// error instead of an unbounded read.
func bodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				apierror.Write(w, surfaceFor(r.URL.Path), bodyTooLarge(limit))
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bodyTooLarge is bodyLimit's 413. A handler that meets the MaxBytesReader
// bodyLimit installs answers with it too, so a body over the limit gets the
// same answer whether its length was declared or not.
func bodyTooLarge(limit int64) *apierror.Error {
	return apierror.New(
		http.StatusRequestEntityTooLarge,
		"request body exceeds "+strconv.FormatInt(limit, 10)+" bytes",
	)
}

// normalizeContentType maps the vendor media types Artifactory clients send,
// such as application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json,
// onto application/json so the generated strict decoders accept them. The
// original value is kept in a context value for handlers that care about the
// exact type. application/yaml is left alone; the descriptor PATCH handler is
// hand-written and reads it directly. So is every registry request: a
// manifest's media type (application/vnd.oci.image.manifest.v1+json) is
// part of what the registry stores and serves back.
func normalizeContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if ct != "" && !docker.IsRegistryPath(r.URL.Path) {
			if mt, params, err := mime.ParseMediaType(
				ct,
			); err == nil &&
				strings.HasSuffix(mt, "+json") {
				// ParseMediaType lowercases the type; keep the client's spelling
				// (Artifactory vendor types use CamelCase) for handlers that echo it.
				original := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
				r = r.WithContext(withOriginalContentType(r.Context(), original))
				r.Header.Set("Content-Type", mime.FormatMediaType("application/json", params))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bindingError renders request binding and response rendering failures from
// the generated strict handlers in the right envelope. A MaxBytesError from
// bodyLimit becomes 413 rather than the generic 400.
func bindingError(status int) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierror.Write(
				w,
				surfaceFor(r.URL.Path),
				apierror.New(http.StatusRequestEntityTooLarge, err.Error()),
			)
			return
		}
		// A typed error carries its own status (501 from the generated stubs,
		// 4xx from validation); only untyped errors take the binder's default.
		var typed *apierror.Error
		if errors.As(err, &typed) {
			apierror.Write(w, surfaceFor(r.URL.Path), typed)
			return
		}
		if status >= http.StatusInternalServerError {
			noteCause(r.Context(), err)
		}
		apierror.Write(w, surfaceFor(r.URL.Path), apierror.New(status, err.Error()))
	}
}
