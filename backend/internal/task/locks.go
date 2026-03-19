package task

import (
	"fmt"
	"strconv"
	"sync"
)

func (m *Manager) taskLock(taskID uint) *sync.Mutex {
	lock, _ := m.locks.LoadOrStore(taskID, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mutex
}

func (m *Manager) strategyLock(nodeID uint, policyID *uint) *sync.Mutex {
	key := buildStrategyKey(nodeID, policyID)
	lock, _ := m.strategyLocks.LoadOrStore(key, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mutex
}

func buildStrategyKey(nodeID uint, policyID *uint) string {
	policyPart := "none"
	if policyID != nil {
		policyPart = strconv.FormatUint(uint64(*policyID), 10)
	}
	return fmt.Sprintf("%d:%s", nodeID, policyPart)
}

func (m *Manager) nodeLock(nodeID uint) *sync.Mutex {
	lock, _ := m.nodeLocks.LoadOrStore(nodeID, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mutex
}
