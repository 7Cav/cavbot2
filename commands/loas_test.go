package commands

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/7cav/cavbot2/utils"
)

// A run that ends before it reads the LOAs, like one whose roster came back
// empty, leaves no LOA call running. A call that outlived its run read the
// 7Cav API address while the test's cleanup restored it, and -race failed
// the test (#890).
func TestLOAsCall_StopEndsTheCallBeforeReturning(t *testing.T) {
	var ended atomic.Bool
	call := callLOAsAlongside(context.Background(), func(ctx context.Context) (*utils.LOAsResponse, error) {
		<-ctx.Done()
		ended.Store(true)
		return nil, ctx.Err()
	})

	call.stop()

	if !ended.Load() {
		t.Fatal("stop returned while the LOA call was still running")
	}
}

func TestLOAsCall_WaitReturnsTheAnswer(t *testing.T) {
	want := &utils.LOAsResponse{LOAs: []utils.LOA{{ThreadID: "42"}}}
	call := callLOAsAlongside(context.Background(), func(context.Context) (*utils.LOAsResponse, error) {
		return want, nil
	})
	defer call.stop()

	answer := call.wait()

	if answer.err != nil || answer.loas != want {
		t.Fatalf("wait returned %+v, want the call's LOAs and no error", answer)
	}
}

func TestLOAsCall_WaitReturnsTheError(t *testing.T) {
	failure := errors.New("7Cav API down")
	call := callLOAsAlongside(context.Background(), func(context.Context) (*utils.LOAsResponse, error) {
		return nil, failure
	})
	defer call.stop()

	if answer := call.wait(); !errors.Is(answer.err, failure) {
		t.Fatalf("wait returned error %v, want %v", answer.err, failure)
	}
}
