package util

import "sync"

type SafeMap[K comparable, V any] struct {
	mu *sync.RWMutex
	m  map[K]V
}

func NewSafeMap[K comparable, V any]() *SafeMap[K, V] {
	return &SafeMap[K, V]{
		m:  make(map[K]V),
		mu: &sync.RWMutex{},
	}
}

func (sm *SafeMap[K, V]) GetOk(key K) (V, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	val, ok := sm.m[key]
	return val, ok
}

func (sm *SafeMap[K, V]) Get(key K) V {
	v, _ := sm.GetOk(key)
	return v
}

func (sm *SafeMap[K, V]) Set(key K, value V) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.m[key] = value
}

func (sm *SafeMap[K, V]) Delete(key K) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.m, key)
}

func (sm *SafeMap[K, V]) Snapshot() map[K]V {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	cp := make(map[K]V, len(sm.m))
	for k, v := range sm.m {
		cp[k] = v
	}
	return cp
}
