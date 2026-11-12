package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type SeatStatus string

const (
	SeatAvailable SeatStatus = "available"
	SeatHeld      SeatStatus = "held"
	SeatBooked    SeatStatus = "booked"
)

const (
	holdTTL         = 5 * time.Minute
	payingHoldTTL   = 2 * time.Minute
	maxSeatsPerHold = 8
	maxStressCount  = 500
)

type Seat struct {
	ID        string
	Row       string
	Number    int
	Tier      string
	Price     int64
	Status    SeatStatus
	HoldID    string
	HoldTill  time.Time
	BookingID string
}

type Hold struct {
	ID        string
	SessionID string
	SeatIDs   []string
	CreatedAt time.Time
	ExpiresAt time.Time
	Paying    bool
	Subtotal  int64
	Fee       int64
	Total     int64
}

type Booking struct {
	ID        string
	Code      string
	SessionID string
	SeatIDs   []string
	Subtotal  int64
	Fee       int64
	Amount    int64
	CreatedAt time.Time
}

type EventInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Venue     string `json:"venue"`
	City      string `json:"city"`
	DateLabel string `json:"dateLabel"`
	TimeLabel string `json:"timeLabel"`
	Duration  string `json:"duration"`
	AgeLimit  string `json:"ageLimit"`
}

var eventInfo = EventInfo{
	ID:        "shabha-ye-bist",
	Title:     "شب‌های بیست",
	Artist:    "گروه مینور",
	Venue:     "سالن سپیده",
	City:      "تهران",
	DateLabel: "پنجشنبه ۲۸ آبان ۱۴۰۵",
	TimeLabel: "۲۱:۰۰",
	Duration:  "۹۰ دقیقه",
	AgeLimit:  "+۱۲ سال",
}

type SeatView struct {
	ID     string     `json:"id"`
	Row    string     `json:"row"`
	Number int        `json:"number"`
	Tier   string     `json:"tier"`
	Price  int64      `json:"price"`
	Status SeatStatus `json:"status"`
	Mine   bool       `json:"mine"`
}

type HoldView struct {
	ID        string    `json:"id"`
	SeatIDs   []string  `json:"seatIds"`
	ExpiresAt time.Time `json:"expiresAt"`
	Subtotal  int64     `json:"subtotal"`
	Fee       int64     `json:"fee"`
	Total     int64     `json:"total"`
}

type BookingView struct {
	ID        string     `json:"id"`
	Code      string     `json:"code"`
	Seats     []SeatView `json:"seats"`
	Subtotal  int64      `json:"subtotal"`
	Fee       int64      `json:"fee"`
	Amount    int64      `json:"amount"`
	CreatedAt time.Time  `json:"createdAt"`
	Event     EventInfo  `json:"event"`
}

type Stats struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Held      int `json:"held"`
	Booked    int `json:"booked"`
}

type StateView struct {
	Seats      []SeatView `json:"seats"`
	Rows       []string   `json:"rows"`
	Stats      Stats      `json:"stats"`
	Hold       *HoldView  `json:"hold,omitempty"`
	Version    int64      `json:"version"`
	ServerTime time.Time  `json:"serverTime"`
	Event      EventInfo  `json:"event"`
}

type StressAttempt struct {
	Index int    `json:"index"`
	OK    bool   `json:"ok"`
	Code  string `json:"code"`
}

type StressResult struct {
	SeatID     string          `json:"seatId"`
	Requests   int             `json:"requests"`
	Success    int             `json:"success"`
	Conflicts  int             `json:"conflicts"`
	DurationMS float64         `json:"durationMs"`
	Attempts   []StressAttempt `json:"attempts"`
}

type AppError struct {
	Code    string
	Message string
	Seats   []string
}

func (e *AppError) Error() string { return e.Message }

func errf(code, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

type Store struct {
	mu       sync.Mutex
	seats    map[string]*Seat
	order    []string
	holds    map[string]*Hold
	bookings map[string]*Booking
	settled  map[string]*Booking
	version  int64
	subs     map[int]chan struct{}
	nextSub  int
	clock    func() time.Time
}

func NewStore(soldSeats []string) *Store {
	s := &Store{
		seats:    make(map[string]*Seat),
		holds:    make(map[string]*Hold),
		bookings: make(map[string]*Booking),
		settled:  make(map[string]*Booking),
		subs:     make(map[int]chan struct{}),
		clock:    time.Now,
	}
	sold := make(map[string]bool, len(soldSeats))
	for _, id := range soldSeats {
		sold[id] = true
	}
	rows := []string{"A", "B", "C", "D", "E", "F", "G", "H"}
	for _, row := range rows {
		tier, price := tierFor(row)
		for n := 1; n <= 12; n++ {
			id := fmt.Sprintf("%s%d", row, n)
			status := SeatAvailable
			bookingID := ""
			if sold[id] {
				status = SeatBooked
				bookingID = "seed"
			}
			s.seats[id] = &Seat{
				ID:        id,
				Row:       row,
				Number:    n,
				Tier:      tier,
				Price:     price,
				Status:    status,
				BookingID: bookingID,
			}
			s.order = append(s.order, id)
		}
	}
	s.version = 1
	return s
}

func tierFor(row string) (string, int64) {
	switch row {
	case "A", "B":
		return "ویژه", 1_850_000
	case "C", "D", "E", "F":
		return "میدان", 1_150_000
	default:
		return "بالکن", 780_000
	}
}

func feeFor(subtotal int64) int64 {
	return ((subtotal/20 + 500) / 1000) * 1000
}

func holdPricing(seatIDs []string, seats map[string]*Seat) (subtotal, fee, total int64) {
	for _, id := range seatIDs {
		if seat, ok := seats[id]; ok {
			subtotal += seat.Price
		}
	}
	fee = feeFor(subtotal)
	return subtotal, fee, subtotal + fee
}

func (s *Store) subscribe() (int, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSub++
	ch := make(chan struct{}, 1)
	s.subs[s.nextSub] = ch
	return s.nextSub, ch
}

func (s *Store) unsubscribe(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, id)
}

func (s *Store) notifyLocked() {
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Store) Snapshot(sessionID string) StateView {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	s.expireLocked(now)

	var myHold *Hold
	for _, h := range s.holds {
		if h.SessionID == sessionID && now.Before(h.ExpiresAt) {
			myHold = h
			break
		}
	}

	seats := make([]SeatView, 0, len(s.order))
	stats := Stats{Total: len(s.order)}
	rowsSeen := make(map[string]bool)
	rows := make([]string, 0, 8)

	for _, id := range s.order {
		seat := s.seats[id]
		view := SeatView{
			ID:     seat.ID,
			Row:    seat.Row,
			Number: seat.Number,
			Tier:   seat.Tier,
			Price:  seat.Price,
			Status: seat.Status,
		}
		if seat.Status == SeatHeld && myHold != nil && seat.HoldID == myHold.ID {
			view.Mine = true
		}
		switch seat.Status {
		case SeatAvailable:
			stats.Available++
		case SeatHeld:
			stats.Held++
		case SeatBooked:
			stats.Booked++
		}
		if !rowsSeen[seat.Row] {
			rowsSeen[seat.Row] = true
			rows = append(rows, seat.Row)
		}
		seats = append(seats, view)
	}

	state := StateView{
		Seats:      seats,
		Rows:       rows,
		Stats:      stats,
		Version:    s.version,
		ServerTime: now,
		Event:      eventInfo,
	}
	if myHold != nil {
		hv := holdView(myHold)
		state.Hold = &hv
	}
	return state
}

func holdView(h *Hold) HoldView {
	ids := append([]string(nil), h.SeatIDs...)
	return HoldView{
		ID:        h.ID,
		SeatIDs:   ids,
		ExpiresAt: h.ExpiresAt,
		Subtotal:  h.Subtotal,
		Fee:       h.Fee,
		Total:     h.Total,
	}
}

func (s *Store) bookingViewLocked(b *Booking) BookingView {
	seats := make([]SeatView, 0, len(b.SeatIDs))
	for _, id := range b.SeatIDs {
		if seat, ok := s.seats[id]; ok {
			seats = append(seats, SeatView{
				ID:     seat.ID,
				Row:    seat.Row,
				Number: seat.Number,
				Tier:   seat.Tier,
				Price:  seat.Price,
				Status: seat.Status,
			})
		}
	}
	return BookingView{
		ID:        b.ID,
		Code:      b.Code,
		Seats:     seats,
		Subtotal:  b.Subtotal,
		Fee:       b.Fee,
		Amount:    b.Amount,
		CreatedAt: b.CreatedAt,
		Event:     eventInfo,
	}
}

func (s *Store) expireLocked(now time.Time) {
	changed := false
	for id, h := range s.holds {

		if h.Paying && now.Before(h.ExpiresAt.Add(time.Minute)) {
			continue
		}
		if !now.After(h.ExpiresAt) {
			continue
		}
		for _, seatID := range h.SeatIDs {
			if seat := s.seats[seatID]; seat != nil && seat.Status == SeatHeld && seat.HoldID == id {
				seat.Status = SeatAvailable
				seat.HoldID = ""
				seat.HoldTill = time.Time{}
			}
		}
		delete(s.holds, id)
		changed = true
	}
	if changed {
		s.version++
		s.notifyLocked()
	}
}

func (s *Store) RunJanitor(stop <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			s.expireLocked(s.clock())
			s.mu.Unlock()
		}
	}
}

func (s *Store) Hold(sessionID string, seatIDs []string) (*HoldView, error) {
	seatIDs = dedupe(seatIDs)
	if len(seatIDs) == 0 {
		return nil, errf("invalid_request", "حداقل یک صندلی انتخاب کنید.")
	}
	if len(seatIDs) > maxSeatsPerHold {
		return nil, errf("invalid_request", fmt.Sprintf("حداکثر %d صندلی در هر رزرو قابل انتخاب است.", maxSeatsPerHold))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	s.expireLocked(now)

	for _, h := range s.holds {
		if h.SessionID == sessionID {
			return nil, errf("hold_exists", "برای شما یک رزرو موقت فعال است. ابتدا آن را لغو کنید.")
		}
	}

	taken := make([]string, 0, len(seatIDs))
	for _, id := range seatIDs {
		seat, ok := s.seats[id]
		if !ok {
			return nil, errf("not_found", "صندلی موردنظر پیدا نشد.")
		}
		if seat.Status != SeatAvailable {
			taken = append(taken, id)
		}
	}
	if len(taken) > 0 {
		sort.Strings(taken)
		return nil, &AppError{
			Code:    "seat_taken",
			Message: "چند صندلی در همین لحظه توسط کاربر دیگری انتخاب شد. نقشه به‌روز شد؛ دوباره تلاش کنید.",
			Seats:   taken,
		}
	}

	subtotal, fee, total := holdPricing(seatIDs, s.seats)
	h := &Hold{
		ID:        "h_" + randHex(9),
		SessionID: sessionID,
		SeatIDs:   seatIDs,
		CreatedAt: now,
		ExpiresAt: now.Add(holdTTL),
		Subtotal:  subtotal,
		Fee:       fee,
		Total:     total,
	}
	for _, id := range seatIDs {
		seat := s.seats[id]
		seat.Status = SeatHeld
		seat.HoldID = h.ID
		seat.HoldTill = h.ExpiresAt
	}
	s.holds[h.ID] = h
	s.version++
	s.notifyLocked()

	view := holdView(h)
	return &view, nil
}

func (s *Store) Release(holdID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, done := s.settled[holdID]; done {
		return nil
	}
	h, ok := s.holds[holdID]
	if !ok {
		return nil
	}
	if h.SessionID != sessionID {
		return errf("forbidden", "این رزرو موقت متعلق به شما نیست.")
	}
	if h.Paying {
		return errf("hold_busy", "پرداخت در حال انجام است؛ چند لحظه صبر کنید.")
	}

	for _, seatID := range h.SeatIDs {
		if seat := s.seats[seatID]; seat != nil && seat.Status == SeatHeld && seat.HoldID == h.ID {
			seat.Status = SeatAvailable
			seat.HoldID = ""
			seat.HoldTill = time.Time{}
		}
	}
	delete(s.holds, holdID)
	s.version++
	s.notifyLocked()
	return nil
}

func (s *Store) BeginPayment(holdID, sessionID string) (*HoldView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if b, ok := s.settled[holdID]; ok {
		if b.SessionID != sessionID {
			return nil, errf("forbidden", "این رزرو موقت متعلق به شما نیست.")
		}
		return nil, &alreadyPaidError{booking: b}
	}
	h, ok := s.holds[holdID]
	if !ok {
		return nil, errf("hold_not_found", "رزرو موقت شما منقضی شده است. صندلی‌ها آزاد شدند.")
	}
	if h.SessionID != sessionID {
		return nil, errf("forbidden", "این رزرو موقت متعلق به شما نیست.")
	}
	if h.Paying {
		return nil, errf("hold_busy", "پرداخت قبلی در حال پردازش است.")
	}
	now := s.clock()
	if !now.Before(h.ExpiresAt) {
		return nil, errf("hold_expired", "زمان نگه‌داری صندلی‌ها تمام شده است.")
	}
	h.Paying = true
	h.ExpiresAt = now.Add(payingHoldTTL)
	s.version++
	s.notifyLocked()
	view := holdView(h)
	return &view, nil
}

type alreadyPaidError struct {
	booking *Booking
}

func (e *alreadyPaidError) Error() string { return "already paid" }

func (s *Store) CompletePayment(holdID, sessionID string, success bool) (*BookingView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if b, ok := s.settled[holdID]; ok {
		view := s.bookingViewLocked(b)
		return &view, nil
	}
	h, ok := s.holds[holdID]
	if !ok {
		return nil, errf("hold_not_found", "رزرو موقت شما منقضی شده است. صندلی‌ها آزاد شدند.")
	}
	if h.SessionID != sessionID {
		return nil, errf("forbidden", "این رزرو موقت متعلق به شما نیست.")
	}

	if !success {
		h.Paying = false
		h.ExpiresAt = s.clock().Add(2 * time.Minute)
		s.version++
		s.notifyLocked()
		return nil, errf("payment_failed", "پرداخت تأیید نشد. صندلی‌ها همچنان برای شما نگه داشته شده‌اند؛ دوباره تلاش کنید.")
	}

	for _, id := range h.SeatIDs {
		seat := s.seats[id]
		if seat == nil || seat.Status != SeatHeld || seat.HoldID != h.ID {
			h.Paying = false
			return nil, errf("seat_taken", "یکی از صندلی‌ها دیگر در اختیار شما نیست. لطفاً دوباره انتخاب کنید.")
		}
	}

	now := s.clock()
	b := &Booking{
		ID:        "bk_" + randHex(8),
		Code:      bookingCode(),
		SessionID: sessionID,
		SeatIDs:   append([]string(nil), h.SeatIDs...),
		Subtotal:  h.Subtotal,
		Fee:       h.Fee,
		Amount:    h.Total,
		CreatedAt: now,
	}
	for _, id := range h.SeatIDs {
		seat := s.seats[id]
		seat.Status = SeatBooked
		seat.HoldID = ""
		seat.HoldTill = time.Time{}
		seat.BookingID = b.ID
	}
	delete(s.holds, holdID)
	s.bookings[b.ID] = b
	s.settled[holdID] = b
	s.version++
	s.notifyLocked()

	view := s.bookingViewLocked(b)
	return &view, nil
}

func (s *Store) BookingByCode(code string) (*BookingView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bookings {
		if strings.EqualFold(b.Code, code) {
			view := s.bookingViewLocked(b)
			return &view, true
		}
	}
	return nil, false
}

func (s *Store) SettledBooking(holdID string) (*BookingView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.settled[holdID]
	if !ok {
		return nil, false
	}
	view := s.bookingViewLocked(b)
	return &view, true
}

func (s *Store) Stress(seatID string, count int) (*StressResult, error) {
	if count < 2 {
		count = 2
	}
	if count > maxStressCount {
		count = maxStressCount
	}

	s.mu.Lock()
	seat, ok := s.seats[seatID]
	available := ok && seat.Status == SeatAvailable
	s.mu.Unlock()
	if !ok {
		return nil, errf("not_found", "صندلی موردنظر پیدا نشد.")
	}
	if !available {
		return nil, errf("seat_unavailable", "این صندلی الان آزاد نیست؛ یک صندلی دیگر انتخاب کنید.")
	}

	runID := randHex(4)
	start := make(chan struct{})
	attempts := make([]StressAttempt, count)
	var wg sync.WaitGroup
	var successes int32
	var winMu sync.Mutex
	var winnerHold string
	var winnerSession string

	begin := time.Now()
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			session := "stress_" + runID + "_" + fmt.Sprint(i)
			h, err := s.Hold(session, []string{seatID})
			if err == nil {
				atomic.AddInt32(&successes, 1)
				winMu.Lock()
				winnerHold = h.ID
				winnerSession = session
				winMu.Unlock()
				attempts[i] = StressAttempt{Index: i + 1, OK: true, Code: "ok"}
				return
			}
			code := "error"
			if ae, ok := err.(*AppError); ok {
				code = ae.Code
			}
			attempts[i] = StressAttempt{Index: i + 1, OK: false, Code: code}
		}(i)
	}
	close(start)
	wg.Wait()
	elapsed := time.Since(begin)

	if winnerHold != "" {
		_ = s.Release(winnerHold, winnerSession)
	}

	okCount := int(atomic.LoadInt32(&successes))
	return &StressResult{
		SeatID:     seatID,
		Requests:   count,
		Success:    okCount,
		Conflicts:  count - okCount,
		DurationMS: float64(elapsed.Microseconds()) / 1000.0,
		Attempts:   attempts,
	}, nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {

		panic(err)
	}
	return hex.EncodeToString(buf)
}

const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func bookingCode() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return "SD-" + string(out)
}
