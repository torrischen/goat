package util

import "sync"

type SafeSet[T comparable] struct {
	mu  *sync.RWMutex
	set []T
}

func NewSafeSet[T comparable]() *SafeSet[T] {
	return &SafeSet[T]{
		mu:  &sync.RWMutex{},
		set: make([]T, 0),
	}
}

func (ss *SafeSet[T]) Add(val T) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.set = append(ss.set, val)
}

func (ss *SafeSet[T]) Range(f func(T) bool) []T {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	res := make([]T, 0)

	for _, v := range ss.set {
		if f == nil || f(v) {
			res = append(res, v)
		}
	}

	return res
}

func (ss *SafeSet[T]) Len() int {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	return len(ss.set)
}

func (ss *SafeSet[T]) Truncate() {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.set = make([]T, 0)
}

func (ss *SafeSet[T]) Drop(vals []T) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	for _, v := range vals {
		for i, val := range ss.set {
			if val == v {
				ss.set = append(ss.set[:i], ss.set[i+1:]...)
			}
		}
	}
}
