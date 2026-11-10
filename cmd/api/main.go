package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rabbitmq/amqp091-go"
)

type Quote struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"order_id"`
	AmountCents int64     `json:"amount_cents"`
	Status      string    `json:"status"`
	PaymentID   string    `json:"payment_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
type Event struct {
	ID         string    `json:"event_id"`
	Type       string    `json:"type"`
	SagaID     string    `json:"saga_id"`
	OrderID    string    `json:"order_id"`
	OccurredAt time.Time `json:"occurred_at"`
	Data       any       `json:"data,omitempty"`
}
type App struct {
	db        *sql.DB
	logger    *slog.Logger
	publisher *Publisher
	mercado   *MercadoPago
}
type Publisher struct {
	conn     *amqp091.Connection
	channel  *amqp091.Channel
	exchange string
}
type MercadoPago struct {
	baseURL, token string
	client         *http.Client
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	db, err := sql.Open("pgx", env("DATABASE_URL", "postgres://tech_challenge:tech_challenge@localhost:5432/billing_service?sslmode=disable"))
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		panic(err)
	}
	if err = migrate(ctx, db); err != nil {
		panic(err)
	}
	app := &App{db: db, logger: logger, publisher: newPublisher(logger), mercado: &MercadoPago{baseURL: env("MERCADO_PAGO_URL", "https://api.mercadopago.com"), token: os.Getenv("MERCADO_PAGO_ACCESS_TOKEN"), client: &http.Client{Timeout: 10 * time.Second}}}
	if app.publisher != nil {
		go app.consumeOrders()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", app.health)
	mux.HandleFunc("POST /quotes", app.createQuote)
	mux.HandleFunc("GET /quotes/{id}", app.getQuote)
	mux.HandleFunc("POST /payments", app.createPayment)
	mux.HandleFunc("POST /webhooks/mercadopago", app.webhook)
	logger.Info("billing service started", "port", env("PORT", "8082"))
	_ = http.ListenAndServe(":"+env("PORT", "8082"), withCorrelation(mux))
}
func migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS quotes (id uuid PRIMARY KEY, order_id uuid NOT NULL UNIQUE, amount_cents bigint NOT NULL, status text NOT NULL, payment_id text, created_at timestamptz NOT NULL); CREATE TABLE IF NOT EXISTS outbox (id uuid PRIMARY KEY,event_type text NOT NULL,aggregate_id uuid NOT NULL,payload jsonb NOT NULL,published_at timestamptz);`)
	return err
}
func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, 200, map[string]string{"status": "ok", "service": "billing-service"})
}
func (a *App) createQuote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrderID     string `json:"order_id"`
		AmountCents int64  `json:"amount_cents"`
		SagaID      string `json:"saga_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrderID == "" || in.AmountCents <= 0 {
		jsonResponse(w, 400, map[string]string{"error": "order_id and positive amount_cents are required"})
		return
	}
	q := Quote{ID: uuid.NewString(), OrderID: in.OrderID, AmountCents: in.AmountCents, Status: "PENDING_APPROVAL", CreatedAt: time.Now().UTC()}
	err := a.db.QueryRowContext(r.Context(), `INSERT INTO quotes(id,order_id,amount_cents,status,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(order_id) DO UPDATE SET amount_cents=quotes.amount_cents RETURNING id,order_id,amount_cents,status,created_at`, q.ID, q.OrderID, q.AmountCents, q.Status, q.CreatedAt).Scan(&q.ID, &q.OrderID, &q.AmountCents, &q.Status, &q.CreatedAt)
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": "could not create quote"})
		return
	}
	jsonResponse(w, 201, q)
}
func (a *App) getQuote(w http.ResponseWriter, r *http.Request) {
	var q Quote
	err := a.db.QueryRowContext(r.Context(), `SELECT id,order_id,amount_cents,status,COALESCE(payment_id,''),created_at FROM quotes WHERE id=$1`, r.PathValue("id")).Scan(&q.ID, &q.OrderID, &q.AmountCents, &q.Status, &q.PaymentID, &q.CreatedAt)
	if err != nil {
		jsonResponse(w, 404, map[string]string{"error": "quote not found"})
		return
	}
	jsonResponse(w, 200, q)
}
func (a *App) createPayment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		QuoteID string `json:"quote_id"`
		SagaID  string `json:"saga_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.QuoteID == "" {
		jsonResponse(w, 400, map[string]string{"error": "quote_id is required"})
		return
	}
	var q Quote
	err := a.db.QueryRowContext(r.Context(), `SELECT id,order_id,amount_cents,status,created_at FROM quotes WHERE id=$1`, in.QuoteID).Scan(&q.ID, &q.OrderID, &q.AmountCents, &q.Status, &q.CreatedAt)
	if err != nil {
		jsonResponse(w, 404, map[string]string{"error": "quote not found"})
		return
	}
	paymentID := "simulated-" + uuid.NewString()
	if a.mercado.token != "" {
		paymentID, err = a.mercado.createPreference(r.Context(), q)
		if err != nil {
			jsonResponse(w, 502, map[string]string{"error": "Mercado Pago unavailable"})
			return
		}
	}
	_, err = a.db.ExecContext(r.Context(), `UPDATE quotes SET status='PAYMENT_PENDING',payment_id=$1 WHERE id=$2`, paymentID, q.ID)
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 202, map[string]string{"quote_id": q.ID, "payment_id": paymentID, "status": "PAYMENT_PENDING"})
}
func (a *App) webhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var in struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &in)
	paymentID := in.Data.ID
	if paymentID == "" {
		paymentID = r.URL.Query().Get("data.id")
	}
	if paymentID == "" {
		jsonResponse(w, 400, map[string]string{"error": "payment id is required"})
		return
	}
	var q Quote
	err := a.db.QueryRowContext(r.Context(), `SELECT id,order_id,amount_cents,status,created_at FROM quotes WHERE payment_id=$1`, paymentID).Scan(&q.ID, &q.OrderID, &q.AmountCents, &q.Status, &q.CreatedAt)
	if err != nil {
		jsonResponse(w, 202, map[string]string{"status": "ignored"})
		return
	}
	status := "PAYMENT_PENDING"
	eventType := "PaymentPending"
	if in.Status == "approved" || in.Type == "payment.approved" {
		status = "PAID"
		eventType = "PaymentConfirmed"
	}
	_, _ = a.db.ExecContext(r.Context(), `UPDATE quotes SET status=$1 WHERE id=$2`, status, q.ID)
	e := Event{ID: uuid.NewString(), Type: eventType, OrderID: q.OrderID, OccurredAt: time.Now().UTC(), Data: map[string]string{"payment_id": paymentID, "quote_id": q.ID}}
	a.publish(e)
	jsonResponse(w, 200, map[string]string{"status": "processed"})
}
func (a *App) publish(e Event) {
	if a.publisher == nil {
		a.logger.Info("event queued", "event_type", e.Type, "order_id", e.OrderID)
		return
	}
	b, _ := json.Marshal(e)
	_ = a.publisher.channel.PublishWithContext(context.Background(), a.publisher.exchange, "billing."+e.Type, false, false, amqp091.Publishing{ContentType: "application/json", Body: b, MessageId: e.ID, Type: e.Type, CorrelationId: e.SagaID})
}
func (a *App) consumeOrders() {
	ch, err := a.publisher.conn.Channel()
	if err != nil {
		return
	}
	defer ch.Close()
	_, _ = ch.QueueDeclare("billing-service-orders", true, false, false, false, nil)
	_ = ch.QueueBind("billing-service-orders", "order.ServiceOrderCreated", "tech-challenge.events", false, nil)
	msgs, err := ch.Consume("billing-service-orders", "billing-service", false, false, false, false, nil)
	if err != nil {
		return
	}
	for msg := range msgs {
		var e Event
		if json.Unmarshal(msg.Body, &e) == nil {
			if data, ok := e.Data.(map[string]any); ok {
				amount, _ := data["amount_cents"].(float64)
				_, _ = a.db.Exec(`INSERT INTO quotes(id,order_id,amount_cents,status,created_at) VALUES($1,$2,$3,'PENDING_APPROVAL',$4) ON CONFLICT(order_id) DO NOTHING`, uuid.New(), e.OrderID, int64(amount), time.Now().UTC())
			}
		}
		_ = msg.Ack(false)
	}
}
func (m *MercadoPago) createPreference(ctx context.Context, q Quote) (string, error) {
	payload := map[string]any{"items": []any{map[string]any{"title": "Orçamento da OS " + q.OrderID, "quantity": 1, "unit_price": float64(q.AmountCents) / 100}}}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/checkout/preferences", strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := m.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("Mercado Pago status %d", res.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}
func newPublisher(logger *slog.Logger) *Publisher {
	u := os.Getenv("RABBITMQ_URL")
	if u == "" {
		return nil
	}
	c, e := amqp091.Dial(u)
	if e != nil {
		logger.Error("rabbitmq unavailable", "error", e)
		return nil
	}
	ch, e := c.Channel()
	if e != nil {
		return nil
	}
	_ = ch.ExchangeDeclare("tech-challenge.events", "topic", true, false, false, false, nil)
	return &Publisher{conn: c, channel: ch, exchange: "tech-challenge.events"}
}
func withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Correlation-ID", id)
		next.ServeHTTP(w, r)
	})
}
func jsonResponse(w http.ResponseWriter, s int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s)
	_ = json.NewEncoder(w).Encode(v)
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
