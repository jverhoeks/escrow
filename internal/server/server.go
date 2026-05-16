package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/jverhoeks/escrow/internal/metrics"
)

type Server struct {
	http   *http.Server
	router *chi.Mux
	log    zerolog.Logger
}

func New(host string, port int, log zerolog.Logger, storageBackend string) *Server {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)
			next.ServeHTTP(ww, req)
			log.Debug().
				Str("method", req.Method).
				Str("path", req.URL.Path).
				Int("status", ww.Status()).
				Dur("ms", time.Since(start)).
				Msg("request")
		})
	})
	r.Get("/healthz", metrics.HealthHandler(storageBackend))
	r.Handle("/metrics", metrics.MetricsHandler())

	s := &Server{router: r, log: log}
	s.http = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return s
}

func (s *Server) Router() *chi.Mux { return s.router }

func (s *Server) Start() error {
	s.log.Info().Str("addr", s.http.Addr).Msg("sentinel listening")
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
