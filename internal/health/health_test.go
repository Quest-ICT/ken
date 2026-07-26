package health

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckUP(t *testing.T) {
	c := New(t.TempDir())
	c.AddPing("db", func(context.Context) error { return nil })
	rep := c.Check(context.Background())
	if rep.Status != "UP" {
		t.Fatalf("status = %q, want UP", rep.Status)
	}
	if rep.Components["db"].Status != "UP" || rep.Components["storage"].Status != "UP" {
		t.Fatalf("components not UP: %+v", rep.Components)
	}
}

func TestCheckDownOnPingFailure(t *testing.T) {
	c := New(t.TempDir())
	c.AddPing("db", func(context.Context) error { return errors.New("unreachable") })
	rep := c.Check(context.Background())
	if rep.Status != "DOWN" {
		t.Fatalf("status = %q, want DOWN", rep.Status)
	}
	if rep.Components["db"].Status != "DOWN" {
		t.Fatalf("db component = %q, want DOWN", rep.Components["db"].Status)
	}
}

func TestCheckDownOnUnwritableDir(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "missing"))
	rep := c.Check(context.Background())
	if rep.Status != "DOWN" || rep.Components["storage"].Status != "DOWN" {
		t.Fatalf("expected storage DOWN for a missing dir, got %+v", rep)
	}
}

func TestWriteJSONStripsDetailsWhenAnonymous(t *testing.T) {
	dir := t.TempDir()
	rep := New(dir).Check(context.Background())

	var pub, op strings.Builder
	rep.WriteJSON(&pub, false)
	rep.WriteJSON(&op, true)

	if strings.Contains(pub.String(), dir) {
		t.Errorf("public /health leaked the data-dir path:\n%s", pub.String())
	}
	if !strings.Contains(op.String(), dir) {
		t.Errorf("operator /health should include the path:\n%s", op.String())
	}
	if !strings.Contains(pub.String(), `"status": "UP"`) {
		t.Errorf("public /health should still show status:\n%s", pub.String())
	}
}
