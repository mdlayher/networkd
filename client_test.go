package networkd_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/mdlayher/networkd"
)

func TestIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := networkd.Dial(ctx)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("skipping, systemd-networkd is not running: %v", err)
		}

		t.Fatalf("failed to dial: %v", err)
	}
	defer c.Close()

	links, err := c.ListLinks(ctx)
	if err != nil {
		t.Fatalf("failed to list links: %v", err)
	}

	for _, l := range links {
		t.Logf("link: %+v", l)
	}
}
