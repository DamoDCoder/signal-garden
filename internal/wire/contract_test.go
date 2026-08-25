package wire_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	gardenv1 "github.com/damodbear/signal-garden/internal/gen/signal/garden/v1"
)

// TestEvery64BitFieldDeclaresItsJSType keeps the int64 rule from rotting.
//
// A 64-bit field encodes as a JSON *string* under the protobuf JSON mapping,
// because JSON numbers lose precision above 2^53. That is not negotiable, so
// the contract declares what a client should see instead of leaving it to
// whichever generator someone reaches for: jstype = JS_NUMBER for bounded
// counters and offsets a client does arithmetic on, JS_STRING for opaque
// tokens.
//
// Without this test the rule is a paragraph in a decision record, and the
// eleventh int64 field added in a hurry quietly breaks it. See
// docs/decisions/0012-declare-the-js-type-of-every-64-bit-field.md.
func TestEvery64BitFieldDeclaresItsJSType(t *testing.T) {
	// opaque lists the fields that are 64-bit tokens rather than quantities.
	// A seed is an arbitrary value nobody does maths on, and it is the only
	// field in this contract that can legitimately exceed 2^53.
	opaque := map[string]bool{
		"signal.garden.v1.Run.seed":             true,
		"signal.garden.v1.StartRunRequest.seed": true,
	}

	messages := gardenv1.File_signal_garden_v1_garden_proto.Messages()
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		fields := message.Fields()
		for j := 0; j < fields.Len(); j++ {
			field := fields.Get(j)
			if !is64Bit(field.Kind()) {
				continue
			}
			// protoc refuses jstype on a map field, so a map of
			// 64-bit values is the one place the rule cannot reach.
			// ProcessorStats.by_type is the only one, and a client
			// reads its values as strings.
			if field.IsMap() {
				continue
			}

			name := string(field.FullName())
			want := descriptorpb.FieldOptions_JS_NUMBER
			if opaque[name] {
				want = descriptorpb.FieldOptions_JS_STRING
			}

			options, _ := field.Options().(*descriptorpb.FieldOptions)
			switch got := options.GetJstype(); {
			case got == descriptorpb.FieldOptions_JS_NORMAL:
				t.Errorf("%s is 64-bit and declares no jstype; a client would have to guess whether it is a quantity or a token", name)
			case got != want:
				t.Errorf("%s declares jstype %s, want %s", name, got, want)
			}
		}
	}
}

func is64Bit(k protoreflect.Kind) bool {
	switch k {
	case protoreflect.Int64Kind, protoreflect.Uint64Kind, protoreflect.Sint64Kind,
		protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		return true
	default:
		return false
	}
}
