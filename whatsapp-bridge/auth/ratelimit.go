package auth

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// loginLimiter holds per-IP token buckets for /auth/login.
type loginLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    rate.Limit
	burst    int
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// parseRate parses values like "5/1m", "10/30s", "100/1h".
// Returns (rate.Limit, burst, error).
func parseRate(spec string) (rate.Limit, int, error) {
	if spec == "" {
		return rate.Every(12 * time.Second), 5, nil // default: 5/1m
	}
	parts := strings.SplitN(spec, "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid AUTH_LOGIN_RATE %q: want <count>/<window>", spec)
	}
	count, err := strconv.Atoi(parts[0])
	if err != nil || count <= 0 {
		return 0, 0, fmt.Errorf("invalid AUTH_LOGIN_RATE count in %q", spec)
	}
	window, err := time.ParseDuration(parts[1])
	if err != nil || window <= 0 {
		return 0, 0, fmt.Errorf("invalid AUTH_LOGIN_RATE window in %q", spec)
	}
	return rate.Every(window / time.Duration(count)), count, nil
}

func newLoginLimiter(spec string) (*loginLimiter, error) {
	limit, burst, err := parseRate(spec)
	if err != nil {
		return nil, err
	}
	l := &loginLimiter{
		visitors: make(map[string]*visitor),
		limit:    limit,
		burst:    burst,
	}
	go l.evictLoop()
	return l, nil
}

func (l *loginLimiter) get(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.visitors[ip]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.visitors[ip] = v
	}
	v.lastSeen = time.Now()
	return v.limiter
}

func (l *loginLimiter) evictLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		l.mu.Lock()
		for ip, v := range l.visitors {
			if v.lastSeen.Before(cutoff) {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}

// retryAfterSeconds rounds up to the next whole second the limiter expects to refill.
func retryAfterSeconds(lim *rate.Limiter) int {
	r := lim.Reserve()
	defer r.Cancel()
	d := r.Delay()
	if d <= 0 {
		return 1
	}
	secs := int(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	return secs
}

// clientIP extracts the bare IP (no port) from r.RemoteAddr.
// X-Forwarded-For is intentionally NOT consulted (see spec section 2,
// "Known limitation"). A future PR adds trusted-proxy parsing.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
