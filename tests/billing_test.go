package tests

import (
	"testing"

	"github.com/gustavodetoni/tech-challenge-billing-service/internal/application"
)

func TestValidateQuote(t *testing.T) {
	cases := []struct {
		name, orderID string
		amount        int64
		wantErr       bool
	}{{"valid", "order", 100, false}, {"missing order", "", 100, true}, {"invalid amount", "order", 0, true}, {"negative amount", "order", -1, true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if (application.ValidateQuote(tc.orderID, tc.amount) != nil) != tc.wantErr {
				t.Fatalf("unexpected validation result")
			}
		})
	}
}

func TestPaymentStatusFromWebhook(t *testing.T) {
	cases := []struct{ status, event, wantStatus, wantEvent string }{{"approved", "payment.created", "PAID", "PaymentConfirmed"}, {"", "payment.approved", "PAID", "PaymentConfirmed"}, {"rejected", "payment.updated", "PAYMENT_FAILED", "PaymentFailed"}, {"pending", "payment.updated", "PAYMENT_PENDING", "PaymentPending"}}
	for _, tc := range cases {
		t.Run(tc.wantEvent, func(t *testing.T) {
			gotStatus, gotEvent := application.PaymentStatusFromWebhook(tc.status, tc.event)
			if gotStatus != tc.wantStatus || gotEvent != tc.wantEvent {
				t.Fatalf("got %s/%s want %s/%s", gotStatus, gotEvent, tc.wantStatus, tc.wantEvent)
			}
		})
	}
}

func TestPreferenceAmount(t *testing.T) {
	if got := application.PreferenceAmount(15000); got != 150 {
		t.Fatalf("got %v want 150", got)
	}
}
