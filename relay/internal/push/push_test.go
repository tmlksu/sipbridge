package push_test

import (
	"context"
	"testing"

	"github.com/tmlksu/sipbridge/relay/internal/push"
)

func TestNoop(t *testing.T) {
	var p push.Pusher = push.Noop{}
	if err := p.Send(context.Background(), []string{"a"}, push.Payload{Type: "incoming"}); err != nil {
		t.Errorf("Noop は nil を返すはず: %v", err)
	}
}
