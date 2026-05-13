package wastate

import "sync"

// State tracks the WhatsApp client's connection + login state, the
// current pairing QR PNG bytes (populated only while pairing is required),
// and the most recent WhatsApp Web client version string applied to the store.
type State struct {
	mu           sync.RWMutex
	connected    bool
	loggedIn     bool
	pairingQRPNG []byte
	waVersion    string
}

func New() *State {
	return &State{}
}

func (s *State) Connected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected
}

func (s *State) LoggedIn() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loggedIn
}

// PairingQRPNG returns a copy of the current QR PNG bytes, or nil if
// pairing is not required. Returning a copy avoids aliasing issues if
// the state is updated concurrently.
func (s *State) PairingQRPNG() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pairingQRPNG == nil {
		return nil
	}
	out := make([]byte, len(s.pairingQRPNG))
	copy(out, s.pairingQRPNG)
	return out
}

// PairingRequired returns true when the client is not logged in AND
// a pairing QR is currently available.
func (s *State) PairingRequired() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.loggedIn && s.pairingQRPNG != nil
}

func (s *State) SetConnected(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = v
}

func (s *State) SetLoggedIn(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loggedIn = v
}

func (s *State) SetPairingQRPNG(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairingQRPNG = b
}

func (s *State) ClearPairingQR() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairingQRPNG = nil
}

func (s *State) WAVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.waVersion
}

func (s *State) SetWAVersion(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waVersion = v
}
