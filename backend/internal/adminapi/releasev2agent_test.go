package adminapi

import (
	"testing"
	"time"
)

func TestEventTicketIsSingleUseAndRunBound(t *testing.T) {
	api := &API{eventTickets: map[string]eventTicket{}}
	ticket := "rev_test"
	api.eventTickets[ticket] = eventTicket{runID: "rrun_a", expiresAt: time.Now().Add(time.Minute)}

	if api.consumeEventTicket(ticket, "rrun_b") {
		t.Fatal("ticket must not work for another run")
	}
	if !api.consumeEventTicket(ticket, "rrun_a") {
		t.Fatal("ticket should be accepted for its run")
	}
	if api.consumeEventTicket(ticket, "rrun_a") {
		t.Fatal("ticket must be single use")
	}
}

func TestEventTicketExpires(t *testing.T) {
	api := &API{eventTickets: map[string]eventTicket{}}
	api.eventTickets["rev_expired"] = eventTicket{runID: "rrun_a", expiresAt: time.Now().Add(-time.Second)}
	if api.consumeEventTicket("rev_expired", "rrun_a") {
		t.Fatal("expired ticket must be rejected")
	}
	if api.consumeEventTicket("missing", "rrun_a") {
		t.Fatal("unknown ticket must be rejected")
	}
}
