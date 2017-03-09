// Package synctrigger provides a duplicate function trigger suppression
// mechanism.
package synctrigger

import "sync"

// Group represents a class of work and forms a namespace in which
// units of work can be triggered with duplicate suppression.
type Group struct {
	mu sync.Mutex          // protects m
	m  map[string]struct{} // lazily initialized
}

// Go triggers executing the given function and returns immediately, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in the call is ignored.
// It also returns true, if the work has been started and false if it has been ignored.
func (g *Group) Go(key string, fn func()) bool {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]struct{})
	}
	if _, ok := g.m[key]; ok {
		g.mu.Unlock()
		return false
	}
	g.m[key] = struct{}{}
	g.mu.Unlock()

	go func() {
		fn()

		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
	}()
	return true
}
