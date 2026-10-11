package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// recoverPanics turns a panic in any handler into a 500 response instead of
// crashing the whole server and every other request with it.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code a handler wrote, because
// http.ResponseWriter doesn't let you read it back afterwards.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logRequests writes one log line per request: method, path, status, time taken.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"client", clientIP(r))
	})
}

// securityHeaders sets defensive response headers on everything we serve.
//   - Content-Security-Policy: the dashboard may only load scripts, styles and
//     data from this same server. Even if an attacker managed to inject a
//     <script> tag, the browser would refuse to run it.
//   - X-Content-Type-Options: stops browsers guessing a file is something else
//     (e.g. treating a JSON response as HTML).
//   - Referrer-Policy: don't leak full URLs to other sites.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// rateLimit rejects clients that send requests faster than the limiter allows.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "too many requests; slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the IP address of whoever opened the connection.
//
// Note: behind a reverse proxy or load balancer, this is the proxy's address.
// The real client is then in the X-Forwarded-For header, but that header can be
// forged by anyone, so only trust it once you know a proxy you control sets it.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter is a "token bucket" per client.
//
// Picture each client holding a bucket of tokens. Every request spends one.
// Tokens drip back in at a fixed rate, up to the bucket's size. A client can
// burst briefly (spend a full bucket at once), but over time can't go faster
// than the refill rate. An empty bucket means a 429 Too Many Requests.
type rateLimiter struct {
	mu      sync.Mutex // the map is shared by every request goroutine; guard it
	buckets map[string]*bucket
	rate    float64 // tokens added per second
	burst   float64 // bucket size
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time // when tokens was last updated
}

func newRateLimiter(ratePerSec, burst float64, now func() time.Time) *rateLimiter {
	return &rateLimiter{buckets: map[string]*bucket{}, rate: ratePerSec, burst: burst, now: now}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b, ok := rl.buckets[key]
	if !ok {
		if len(rl.buckets) > 10_000 {
			rl.sweep(now) // forget idle clients so the map can't grow forever
		}
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}

	// Add the tokens that dripped in since this client's last request.
	b.tokens = min(rl.burst, b.tokens+now.Sub(b.last).Seconds()*rl.rate)
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets that have refilled completely; those clients have been
// idle long enough that forgetting them changes nothing.
func (rl *rateLimiter) sweep(now time.Time) {
	full := time.Duration(rl.burst / rl.rate * float64(time.Second))
	for k, b := range rl.buckets {
		if now.Sub(b.last) > full {
			delete(rl.buckets, k)
		}
	}
}
