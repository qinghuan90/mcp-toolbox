//go:build desktop && darwin

package desktoptray

/*
#cgo LDFLAGS: -framework Cocoa
void toolboxTrayStart(void);
void toolboxTrayStop(void);
void toolboxRequestQuit(void);
*/
import "C"
import "sync"

var mu sync.RWMutex
var actionHandler func(int)

func Start(handler func(int)) { mu.Lock(); actionHandler = handler; mu.Unlock(); C.toolboxTrayStart() }
func Stop()                   { C.toolboxTrayStop(); mu.Lock(); actionHandler = nil; mu.Unlock() }

//export toolboxTrayAction
func toolboxTrayAction(action C.int) {
	mu.RLock()
	handler := actionHandler
	mu.RUnlock()
	if handler != nil {
		go handler(int(action))
	}
}

func Supported() bool { return true }

// Use the same native termination path as Dock Quit and macOS system requests.
func RequestQuit() bool { C.toolboxRequestQuit(); return true }
