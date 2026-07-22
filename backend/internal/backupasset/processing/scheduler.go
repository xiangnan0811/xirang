package processing

import "sort"

type SlotClass string

const (
	SlotInteractive        SlotClass = "interactive"
	SlotBackground         SlotClass = "background"
	SlotBackgroundBorrowed SlotClass = "background_borrowed"
)

type SlotUsage struct {
	InteractiveTotal int
	InteractiveUsed  int
	BackgroundTotal  int
	BackgroundUsed   int
}

func ChooseSlot(usage SlotUsage, priority PriorityClass) (SlotClass, bool) {
	if usage.InteractiveTotal < 0 || usage.InteractiveUsed < 0 || usage.BackgroundTotal < 0 || usage.BackgroundUsed < 0 ||
		usage.InteractiveUsed > usage.InteractiveTotal || usage.BackgroundUsed > usage.BackgroundTotal {
		return "", false
	}
	switch priority {
	case PriorityInteractive:
		if usage.InteractiveUsed < usage.InteractiveTotal {
			return SlotInteractive, true
		}
		if usage.BackgroundUsed < usage.BackgroundTotal {
			return SlotBackgroundBorrowed, true
		}
	case PriorityBackground:
		if usage.BackgroundUsed < usage.BackgroundTotal {
			return SlotBackground, true
		}
	}
	return "", false
}

type QueueCandidate struct {
	ID             string
	PriorityClass  PriorityClass
	Priority       int
	QueuedUnixNano int64
}

func SortQueueCandidates(candidates []QueueCandidate) {
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Priority != candidates[right].Priority {
			return candidates[left].Priority > candidates[right].Priority
		}
		leftInteractive := candidates[left].PriorityClass == PriorityInteractive
		rightInteractive := candidates[right].PriorityClass == PriorityInteractive
		if leftInteractive != rightInteractive {
			return leftInteractive
		}
		if candidates[left].QueuedUnixNano != candidates[right].QueuedUnixNano {
			return candidates[left].QueuedUnixNano < candidates[right].QueuedUnixNano
		}
		return candidates[left].ID < candidates[right].ID
	})
}
