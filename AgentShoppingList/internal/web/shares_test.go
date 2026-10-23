package web

import (
	"testing"
	"time"
)

func TestShareBudgetBoundedBurstAndRefill(t *testing.T) {
	budget := &shareBudget{}
	now := time.Now()
	for range 60 {
		if !budget.allow(now) {
			t.Fatal("initial public burst was smaller than documented")
		}
	}
	if budget.allow(now) || budget.allow(now.Add(time.Second)) {
		t.Fatal("empty public budget admitted a request too early")
	}
	if !budget.allow(now.Add(2*time.Second)) || budget.allow(now.Add(2*time.Second)) {
		t.Fatal("public budget did not refill at 30 requests/minute")
	}
	for range 60 {
		if !budget.allow(now.Add(time.Hour)) {
			t.Fatal("long idle did not refill burst")
		}
	}
	if budget.allow(now.Add(time.Hour)) {
		t.Fatal("idle budget grew beyond its fixed capacity")
	}
}
