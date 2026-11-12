package main

import (
	"context"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var embeddedWeb embed.FS

type ctxKey struct{}

type app struct {
	store  *Store
	logger *log.Logger
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address (use :8080 to expose on the LAN)")
	flag.Parse()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	store := NewStore([]string{
		"A2", "A3", "A8",
		"B5", "B6", "B12",
		"C1", "C7", "C8",
		"D3", "D10",
		"E5", "E6",
		"F2", "F11",
		"G7",
		"H4", "H5",
	})

	sub, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		logger.Fatalf("embed: %v", err)
	}
	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		logger.Fatalf("index: %v", err)
	}

	a := &app{store: store, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", a.handleState)
	mux.HandleFunc("POST /api/hold", a.handleHold)
	mux.HandleFunc("POST /api/release", a.handleRelease)
	mux.HandleFunc("POST /api/pay", a.handlePay)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": store.Snapshot(sessionID(r)).Version})
	})
	static := http.FileServer(http.FS(sub))
	mux.Handle("GET /assets/", cacheAssets(static))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusNotFound, errorBody("not_found", "این مسیر API وجود ندارد.", nil))
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	})

	handler := chain(mux, withSession, withSecurityHeaders, withRecovery(logger), withLogging(logger))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       90 * time.Second,
	}

	stopJanitor := make(chan struct{})
	go store.RunJanitor(stopJanitor)

	go func() {
		logger.Printf("صندلی | Seat reservation demo")
		logger.Printf("listening on http://%s", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	close(stopJanitor)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (a *app) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"state": a.store.Snapshot(sessionID(r)),
	})
}

type holdRequest struct {
	SeatIDs []string `json:"seatIds"`
}

func (a *app) handleHold(w http.ResponseWriter, r *http.Request) {
	var req holdRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeAppError(w, errf("invalid_request", "درخواست نامعتبر است."))
		return
	}
	sid := sessionID(r)
	hold, err := a.store.Hold(sid, req.SeatIDs)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"hold":  hold,
		"state": a.store.Snapshot(sid),
	})
}

type releaseRequest struct {
	HoldID string `json:"holdId"`
}

func (a *app) handleRelease(w http.ResponseWriter, r *http.Request) {
	var req releaseRequest
	if err := decodeJSON(w, r, &req); err != nil || req.HoldID == "" {
		writeAppError(w, errf("invalid_request", "شناسه رزرو موقت ارسال نشده است."))
		return
	}
	sid := sessionID(r)
	if err := a.store.Release(req.HoldID, sid); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"state": a.store.Snapshot(sid),
	})
}

type payRequest struct {
	HoldID          string `json:"holdId"`
	CardNumber      string `json:"cardNumber"`
	CardHolder      string `json:"cardHolder"`
	Expiry          string `json:"expiry"`
	CVV             string `json:"cvv"`
	SimulateFailure bool   `json:"simulateFailure"`
}

func (a *app) handlePay(w http.ResponseWriter, r *http.Request) {
	var req payRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeAppError(w, errf("invalid_request", "اطلاعات پرداخت نامعتبر است."))
		return
	}
	if req.HoldID == "" {
		writeAppError(w, errf("invalid_request", "رزرو موقت پیدا نشد."))
		return
	}
	if err := validateCard(req); err != nil {
		writeAppError(w, err)
		return
	}

	sid := sessionID(r)
	if _, err := a.store.BeginPayment(req.HoldID, sid); err != nil {
		var paid *alreadyPaidError
		if errors.As(err, &paid) {
			if view, ok := a.store.SettledBooking(req.HoldID); ok {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":      true,
					"booking": view,
					"state":   a.store.Snapshot(sid),
				})
				return
			}
		}
		writeAppError(w, err)
		return
	}

	time.Sleep(1100 * time.Millisecond)

	booking, err := a.store.CompletePayment(req.HoldID, sid, !req.SimulateFailure)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"booking": booking,
		"state":   a.store.Snapshot(sid),
	})
}

func (a *app) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAppError(w, errf("stream_unsupported", "این مرورگر از به‌روزرسانی زنده پشتیبانی نمی‌کند."))
		return
	}
	sid := sessionID(r)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	subID, ch := a.store.subscribe()
	defer a.store.unsubscribe(subID)

	fmt.Fprint(w, "retry: 3000\n\n")
	if payload, err := json.Marshal(a.store.Snapshot(sid)); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", payload)
	}
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			payload, err := json.Marshal(a.store.Snapshot(sid))
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func validateCard(req payRequest) *AppError {
	number := strings.NewReplacer(" ", "", "-", "").Replace(req.CardNumber)
	if len(number) != 16 || !luhnOK(number) {
		return errf("invalid_card", "شماره کارت معتبر نیست. برای آزمایش از ۴۲۴۲ ۴۲۴۲ ۴۲۴۲ ۴۲۴۲ استفاده کنید.")
	}
	if strings.TrimSpace(req.CardHolder) == "" {
		return errf("invalid_card", "نام دارنده کارت را وارد کنید.")
	}
	parts := strings.Split(req.Expiry, "/")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return errf("invalid_card", "تاریخ انقضا باید به شکل MM/YY باشد.")
	}
	var month, year int
	if _, err := fmt.Sscanf(parts[0], "%d", &month); err != nil || month < 1 || month > 12 {
		return errf("invalid_card", "ماه تاریخ انقضا معتبر نیست.")
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &year); err != nil {
		return errf("invalid_card", "سال تاریخ انقضا معتبر نیست.")
	}
	year += 2000
	now := time.Now()
	if year < now.Year() || (year == now.Year() && month < int(now.Month())) {
		return errf("invalid_card", "کارت منقضی شده است. برای آزمایش 09/29 را وارد کنید.")
	}
	if len(req.CVV) < 3 || len(req.CVV) > 4 || !digitsOnly(req.CVV) {
		return errf("invalid_card", "CVV2 باید ۳ یا ۴ رقم باشد.")
	}
	return nil
}

func luhnOK(number string) bool {
	if number == "" {
		return false
	}
	sum := 0
	double := false
	for i := len(number) - 1; i >= 0; i-- {
		c := number[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

func digitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

func withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := ""
		if c, err := r.Cookie("seaty_sid"); err == nil && validSession(c.Value) {
			sid = c.Value
		}
		if sid == "" {
			sid = "s_" + randHex(16)
			http.SetCookie(w, &http.Cookie{
				Name:     "seaty_sid",
				Value:    sid,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   7 * 24 * 60 * 60,
			})
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sid)))
	})
}

func validSession(s string) bool {
	if len(s) != 34 || !strings.HasPrefix(s, "s_") {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

func sessionID(r *http.Request) string {
	sid, _ := r.Context().Value(ctxKey{}).(string)
	return sid
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func withRecovery(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Printf("panic: %v", rec)
					writeAppError(w, errf("internal", "خطای داخلی سرور. دوباره تلاش کنید."))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func withLogging(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			if r.URL.Path == "/api/events" {
				return
			}
			logger.Printf("%-6s %-22s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		})
	}
}

func cacheAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func errorBody(code, message string, seats []string) map[string]any {
	e := map[string]any{"code": code, "message": message}
	if len(seats) > 0 {
		e["seats"] = seats
	}
	return map[string]any{"ok": false, "error": e}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAppError(w http.ResponseWriter, err error) {
	var ae *AppError
	if !errors.As(err, &ae) {
		ae = errf("internal", "خطای داخلی سرور. دوباره تلاش کنید.")
	}
	status := http.StatusBadRequest
	switch ae.Code {
	case "not_found":
		status = http.StatusNotFound
	case "forbidden":
		status = http.StatusForbidden
	case "seat_taken", "seat_unavailable", "hold_exists", "hold_busy", "hold_expired":
		status = http.StatusConflict
	case "hold_not_found":
		status = http.StatusGone
	case "payment_failed":
		status = http.StatusPaymentRequired
	case "internal":
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, errorBody(ae.Code, ae.Message, ae.Seats))
}
