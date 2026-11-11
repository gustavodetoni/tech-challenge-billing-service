package application

import "fmt"

func ValidateQuote(orderID string, amountCents int64) error {
	if orderID == "" {
		return fmt.Errorf("order_id is required")
	}
	if amountCents <= 0 {
		return fmt.Errorf("amount_cents must be positive")
	}
	return nil
}

func PaymentStatusFromWebhook(status, eventType string) (string, string) {
	if status == "approved" || eventType == "payment.approved" {
		return "PAID", "PaymentConfirmed"
	}
	if status == "rejected" || eventType == "payment.rejected" {
		return "PAYMENT_FAILED", "PaymentFailed"
	}
	return "PAYMENT_PENDING", "PaymentPending"
}

func PreferenceAmount(amountCents int64) float64 { return float64(amountCents) / 100 }

func CompensationForPayment(status string) string {
	if status == "PAID" || status == "PAYMENT_PENDING" {
		return "REFUND_PAYMENT"
	}
	return "NO_COMPENSATION"
}
