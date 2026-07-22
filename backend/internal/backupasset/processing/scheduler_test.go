package processing

import "testing"

func TestSchedulerPreservesInteractiveReserve(t *testing.T) {
	usage := SlotUsage{InteractiveTotal: 2, InteractiveUsed: 0, BackgroundTotal: 2, BackgroundUsed: 2}
	if _, ok := ChooseSlot(usage, PriorityBackground); ok {
		t.Fatal("background work consumed an interactive reserved slot")
	}
	slot, ok := ChooseSlot(usage, PriorityInteractive)
	if !ok || slot != SlotInteractive {
		t.Fatalf("interactive work did not receive reserve: slot=%q ok=%t", slot, ok)
	}
}

func TestSchedulerAllowsInteractiveToBorrowIdleBackground(t *testing.T) {
	usage := SlotUsage{InteractiveTotal: 2, InteractiveUsed: 2, BackgroundTotal: 2, BackgroundUsed: 1}
	slot, ok := ChooseSlot(usage, PriorityInteractive)
	if !ok || slot != SlotBackgroundBorrowed {
		t.Fatalf("interactive work did not borrow idle background: slot=%q ok=%t", slot, ok)
	}
	if _, ok := ChooseSlot(SlotUsage{InteractiveTotal: 2, InteractiveUsed: 2, BackgroundTotal: 2, BackgroundUsed: 2}, PriorityInteractive); ok {
		t.Fatal("interactive work exceeded total capacity")
	}
}

func TestSchedulerStableQueueOrder(t *testing.T) {
	jobs := []QueueCandidate{
		{ID: "latest", PriorityClass: PriorityBackground, Priority: 950, QueuedUnixNano: 40},
		{ID: "c", PriorityClass: PriorityBackground, Priority: 10, QueuedUnixNano: 20},
		{ID: "b", PriorityClass: PriorityInteractive, Priority: 10, QueuedUnixNano: 20},
		{ID: "a", PriorityClass: PriorityInteractive, Priority: 10, QueuedUnixNano: 20},
		{ID: "d", PriorityClass: PriorityInteractive, Priority: 20, QueuedUnixNano: 30},
		{ID: "e", PriorityClass: PriorityInteractive, Priority: 20, QueuedUnixNano: 10},
	}
	SortQueueCandidates(jobs)
	want := []string{"latest", "e", "d", "a", "b", "c"}
	for index := range want {
		if jobs[index].ID != want[index] {
			t.Fatalf("queue order=%v, want %v", jobs, want)
		}
	}
}
