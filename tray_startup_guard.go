package main

import "sync"

// trayStartupGuard orders close-handler installation against an early exit.
type trayStartupGuard struct {
	mu            sync.Mutex
	exitRequested bool
}

func (g *trayStartupGuard) installCloseHandler(install func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exitRequested {
		return false
	}
	install()
	return true
}

func (g *trayStartupGuard) requestExit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.exitRequested {
		return false
	}
	g.exitRequested = true
	return true
}
