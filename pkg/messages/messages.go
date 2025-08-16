package messages

import (
	"github.com/lytics/grid/v3"
	"google.golang.org/protobuf/types/known/structpb"
)

// Register common protobuf Struct so all messages can be generic JSON-like payloads.
func init() {
	// IMPORTANT: register value type, not pointer, per grid codec expectations
	_ = grid.Register(structpb.Struct{})
}
