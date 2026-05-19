package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/jverhoeks/escrow/internal/metrics"
)

func ecoFromPath(p string) string {
	switch {
	case strings.HasPrefix(p, "/pypi/"):
		return "pypi"
	case strings.HasPrefix(p, "/go/"):
		return "go"
	case strings.HasPrefix(p, "/cargo/"):
		return "cargo"
	case strings.HasPrefix(p, "/composer/"):
		return "composer"
	case strings.HasPrefix(p, "/nuget/"):
		return "nuget"
	case strings.HasPrefix(p, "/maven2/"):
		return "maven"
	case strings.HasPrefix(p, "/healthz"), strings.HasPrefix(p, "/metrics"), strings.HasPrefix(p, "/dashboard"):
		return ""
	default:
		return "npm"
	}
}

// Options configures the HTTP server.
type Options struct {
	Version                  string
	Host                     string
	Port                     int
	StorageBackend           string
	CacheDir                 string // disk cache root for health probe writability check; empty for non-disk
	WriteTimeoutSeconds      int
	ReadHeaderTimeoutSeconds int
	IdleTimeoutSeconds       int
	TLSCertFile              string
	TLSKeyFile               string
	ProxyRateLimitPerMin     int
	// UpstreamURLs maps ecosystem name → base URL for upstream health probes.
	UpstreamURLs map[string]string
}

type Server struct {
	http     *http.Server
	router   *chi.Mux
	log      zerolog.Logger
	certFile string
	keyFile  string
	rl       *ipRateLimiter // may be nil
}

func New(opts Options, log zerolog.Logger) *Server {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
			if opts.TLSCertFile != "" {
				// HSTS: tell browsers to always use HTTPS. max-age=2 years.
				w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
			next.ServeHTTP(ww, req)
			ev := log.Debug().
				Str("method", req.Method).
				Str("path", req.URL.Path).
				Int("status", ww.Status()).
				Int("bytes", ww.BytesWritten()).
				Dur("ms", time.Since(start))
			if eco := ecoFromPath(req.URL.Path); eco != "" {
				ev = ev.Str("eco", eco)
			}
			ev.Msg("request")
		})
	})
	s := &Server{router: r, log: log, certFile: opts.TLSCertFile, keyFile: opts.TLSKeyFile}
	if opts.ProxyRateLimitPerMin > 0 {
		s.rl = newIPRateLimiter(opts.ProxyRateLimitPerMin)
		r.Use(s.rl.middleware())
		log.Info().Int("limit_per_min", opts.ProxyRateLimitPerMin).Msg("proxy rate limiting enabled")
	}
	r.Get("/healthz", metrics.HealthHandler(opts.Version, opts.StorageBackend, opts.UpstreamURLs, opts.CacheDir))
	r.Handle("/metrics", metrics.MetricsHandler())

	writeTimeout := time.Duration(opts.WriteTimeoutSeconds) * time.Second
	if writeTimeout == 0 {
		writeTimeout = 120 * time.Second
	}

	readHeaderTimeout := time.Duration(opts.ReadHeaderTimeoutSeconds) * time.Second
	if readHeaderTimeout == 0 {
		readHeaderTimeout = 10 * time.Second
	}
	idleTimeout := time.Duration(opts.IdleTimeoutSeconds) * time.Second
	if idleTimeout == 0 {
		idleTimeout = 120 * time.Second
	}

	s.http = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", opts.Host, opts.Port),
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	return s
}

func (s *Server) Router() *chi.Mux { return s.router }

func (s *Server) Start() error {
	s.log.Info().Str("addr", s.http.Addr).Msg("escrow listening")
	if s.certFile != "" && s.keyFile != "" {
		s.log.Info().Str("cert", s.certFile).Msg("TLS enabled")
		return s.http.ListenAndServeTLS(s.certFile, s.keyFile)
	}
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.rl != nil {
		s.rl.stop()
	}
	return s.http.Shutdown(ctx)
}
