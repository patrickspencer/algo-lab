package server

import (
	"testing"

	"github.com/patrickspencer/algo-lab-public/internal/store"
)

func drain(c *conn) []event {
	var out []event
	for {
		select {
		case ev := <-c.ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func lastOfType(evs []event, t string) (event, bool) {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == t {
			return evs[i], true
		}
	}
	return event{}, false
}

func TestHubDuoFlow(t *testing.T) {
	h := newHub()
	alice := store.User{ID: 0, Name: "alice"}
	bob := store.User{ID: 1, Name: "bob"}
	ca := h.connect(alice)
	cb := h.connect(bob)
	h.setPresence(alice, "two-sum", "1. Two Sum", "python3")

	// Both see two people online.
	evs := drain(cb)
	p, ok := lastOfType(evs, "presence")
	if !ok || len(p.Data.(map[string]any)["users"].([]presenceEntry)) != 2 {
		t.Fatalf("presence: %+v", evs)
	}

	// Cannot invite yourself or someone offline.
	if _, err := h.invite(alice, alice, "two-sum", ""); err == nil {
		t.Fatal("self invite allowed")
	}
	if _, err := h.invite(alice, store.User{ID: 9, Name: "ghost"}, "two-sum", ""); err == nil {
		t.Fatal("offline invite allowed")
	}

	inv, err := h.invite(alice, bob, "two-sum", "1. Two Sum")
	if err != nil {
		t.Fatal(err)
	}
	if ev, ok := lastOfType(drain(cb), "invite"); !ok || ev.Data.(*invite).ID != inv.ID {
		t.Fatal("bob did not get the invite")
	}
	// Only the invitee can respond.
	if _, err := h.respond(alice, inv.ID, true); err == nil {
		t.Fatal("inviter accepted their own invite")
	}
	sess, err := h.respond(bob, inv.ID, true)
	if err != nil || sess == nil {
		t.Fatalf("accept: %v %v", sess, err)
	}
	drain(ca)
	if _, ok := lastOfType(drain(cb), "duo-start"); !ok {
		t.Fatal("bob did not get duo-start")
	}

	// Updates go to the partner only.
	if err := h.update(alice, sess.ID, duoState{Code: "x = 1", Cursor: 1, Lang: "python3"}); err != nil {
		t.Fatal(err)
	}
	if ev, ok := lastOfType(drain(cb), "duo-update"); !ok || ev.Data.(map[string]any)["code"] != "x = 1" {
		t.Fatal("bob did not get the update")
	}
	if _, ok := lastOfType(drain(ca), "duo-update"); ok {
		t.Fatal("alice got her own update")
	}
	if _, partner, st, ok := h.partnerState(bob, sess.ID); !ok || partner.ID != alice.ID || st == nil || st.Code != "x = 1" {
		t.Fatalf("partner state: %+v %+v %v", partner, st, ok)
	}
	// While in a duo, neither can be invited again.
	if _, err := h.invite(alice, bob, "two-sum", ""); err == nil {
		t.Fatal("invite during duo allowed")
	}

	// Alice's tab closing ends the duo for bob.
	h.disconnect(ca)
	if ev, ok := lastOfType(drain(cb), "duo-end"); !ok || ev.Data.(map[string]any)["by"].(store.User).ID != alice.ID {
		t.Fatal("bob did not get duo-end")
	}
	if _, _, _, ok := h.currentSession(bob); ok {
		t.Fatal("session still active")
	}
	if err := h.update(bob, sess.ID, duoState{}); err == nil {
		t.Fatal("update to ended session allowed")
	}
}
