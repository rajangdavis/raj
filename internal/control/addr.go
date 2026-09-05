package control

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"strings"
)

// Addressing: one string names either a Unix socket or a TCP endpoint.
//
// The socket was enough while the driver and the editor shared a filesystem.
// They increasingly do not. raj runs in the terminal on the host, because that
// is where the keyboard is and where a terminal actually delivers the chords —
// a raj inside a container is a raj whose keybindings the terminal never sends.
// The agent runs in a container, because that is where it is safe to let it run
// commands. A Unix socket cannot cross that boundary: it is a filesystem
// object, and bind-mounting one does not forward it — the container's kernel
// has no listener behind the inode it sees.
//
// So an address may be `tcp://host:port`. Anything without that prefix is a
// path, so every existing invocation still means what it meant.
//
// # Why TCP needs a token and the socket does not
//
// The Unix socket's authorisation is the filesystem: 0700 directory, 0600
// socket, so anything the user can run can drive the editor — the same trust
// boundary as their own shell. A TCP port has no such thing, and this protocol
// reads and writes the user's unsaved work. Reachable on a port with no check,
// on a laptop on a café network, it is a stranger's editor.
//
// So a TCP listener requires a shared secret on every request. It is not a
// login: there are no sessions, no users and no roles here, and inventing them
// would be inventing an authorisation model for a thing whose model is "you are
// the user". It is the same check the filesystem was doing, moved onto the wire.
const tcpScheme = "tcp://"

// TokenEnv names the shared secret on both sides: the editor reads it to fix
// the token rather than generating one, and a client reads it to present one.
// One variable, not two, because they are the same secret and a pair of names
// invites setting only one of them.
const TokenEnv = "RAJ_CONTROL_TOKEN"

// AddrEnv names the editor to talk to. RAJ_SOCKET is still read, and still
// means the same thing; this exists because "socket" reads as a lie once the
// value is `tcp://host:port`, and a harness author copying an example should
// not have to wonder whether the variable is doing something Unix-specific.
const AddrEnv = "RAJ_CONTROL_ADDR"

// ParseAddr splits a control address into a network and an address for it.
func ParseAddr(s string) (network, address string) {
	if rest, ok := strings.CutPrefix(s, tcpScheme); ok {
		return "tcp", rest
	}
	return "unix", s
}

// IsTCP reports whether an address names a TCP endpoint rather than a path.
func IsTCP(s string) bool { return strings.HasPrefix(s, tcpScheme) }

// TCPAddr renders a resolved listener address back into the form ParseAddr and
// Dial accept, so what the editor prints is what a client can be given.
func TCPAddr(a net.Addr) string { return tcpScheme + a.String() }

// NewToken mints a secret. 32 bytes: long enough that guessing is not a threat
// model worth thinking about, so the server does not need to rate-limit and can
// simply refuse and hang up.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Loopback reports whether a listen address is reachable only from this
// machine. It is the difference between a port the user's own tools can see and
// one the network can, and the editor says which it has taken — binding
// 0.0.0.0 is the right thing to do to reach a container and the wrong thing to
// do by accident.
//
// An empty or wildcard host is not loopback: `:7391` binds every interface.
func Loopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
