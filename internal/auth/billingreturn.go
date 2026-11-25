package auth

import (
	"net/http"
)

// BillingReturnPath is where Stripe Checkout and the customer portal
// (internal/billing) send the browser back to, with ?to=done, canceled or
// portal. The way back starts on Stripe, cross-site, so the SameSite=Strict
// session cookie isn't sent with it: / would look signed out. This public
// hop answers with a page of Dash's own that goes on to /?billing=<to>, a
// same-site navigation that carries the cookie (as Google sign-in's
// refresh does).
const BillingReturnPath = "/billing/return"

// billingReturns are the values ?to= may take: nothing else, so the hop
// can't send anyone elsewhere.
var billingReturns = map[string]bool{"done": true, "canceled": true, "portal": true}

func (a *Auth) billingReturn(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("to")
	if !billingReturns[to] {
		a.page(w, http.StatusBadRequest, hostPageData{Title: "Unknown billing link", Text: "This link back from billing isn't one Dash knows.", Link: "/", LinkText: "Open Dash"})
		return
	}
	a.refresh(w, "Back from billing", "/?billing="+to)
}
