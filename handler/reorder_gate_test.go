package handler

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type gateResult struct {
	applied bool
	err     error
}

func TestKeyOrderWriteGateSkipsOlderRequestThatArrivesLate(t *testing.T) {
	var gate keyOrderWriteGate
	const providerID int64 = 7
	older := keyOrderRevision{Sequence: 1, ClientID: "tab"}
	newer := keyOrderRevision{Sequence: 2, ClientID: "tab"}

	var appliedWrites []string
	if applied, err := gate.apply(providerID, newer, func() error {
		appliedWrites = append(appliedWrites, "newer")
		return nil
	}); err != nil || !applied {
		t.Fatalf("apply newer = (%t, %v), want (true, nil)", applied, err)
	}
	if applied, err := gate.apply(providerID, older, func() error {
		appliedWrites = append(appliedWrites, "older")
		return nil
	}); err != nil || applied {
		t.Fatalf("apply older = (%t, %v), want (false, nil)", applied, err)
	}
	if !reflect.DeepEqual(appliedWrites, []string{"newer"}) {
		t.Fatalf("applied writes = %v, want [newer]", appliedWrites)
	}
}

func TestKeyOrderWriteGateTracksProvidersIndependently(t *testing.T) {
	var gate keyOrderWriteGate
	newer := keyOrderRevision{Sequence: 20, ClientID: "tab"}
	older := keyOrderRevision{Sequence: 10, ClientID: "tab"}
	var writes []int64

	for _, request := range []struct {
		providerID int64
		revision   keyOrderRevision
	}{{providerID: 1, revision: newer}, {providerID: 2, revision: older}} {
		applied, err := gate.apply(request.providerID, request.revision, func() error {
			writes = append(writes, request.providerID)
			return nil
		})
		if err != nil || !applied {
			t.Fatalf("apply provider %d = (%t, %v), want (true, nil)", request.providerID, applied, err)
		}
	}
	if !reflect.DeepEqual(writes, []int64{1, 2}) {
		t.Fatalf("applied provider writes = %v, want [1 2]", writes)
	}
}

func TestKeyOrderWriteGateDoesNotBlockDifferentProviders(t *testing.T) {
	var gate keyOrderWriteGate
	revision := keyOrderRevision{Sequence: 10, ClientID: "tab"}
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	providerADone := make(chan gateResult, 1)
	go func() {
		applied, err := gate.apply(1, revision, func() error {
			close(writeStarted)
			<-releaseWrite
			return nil
		})
		providerADone <- gateResult{applied: applied, err: err}
	}()
	<-writeStarted

	providerBStarted := make(chan struct{})
	providerBDone := make(chan gateResult, 1)
	go func() {
		applied, err := gate.apply(2, revision, func() error {
			close(providerBStarted)
			return nil
		})
		providerBDone <- gateResult{applied: applied, err: err}
	}()
	select {
	case <-providerBStarted:
	case <-time.After(time.Second):
		close(releaseWrite)
		<-providerADone
		t.Fatal("provider B write was blocked by provider A")
	}
	close(releaseWrite)
	if result := <-providerADone; result.err != nil || !result.applied {
		t.Fatalf("apply provider A = (%t, %v), want (true, nil)", result.applied, result.err)
	}
	if result := <-providerBDone; result.err != nil || !result.applied {
		t.Fatalf("apply provider B = (%t, %v), want (true, nil)", result.applied, result.err)
	}
}

func TestKeyOrderWriteGateFinishesActiveWriteBeforeNewerRequest(t *testing.T) {
	var gate keyOrderWriteGate
	const providerID int64 = 7
	older := keyOrderRevision{Sequence: 1, ClientID: "tab"}
	newer := keyOrderRevision{Sequence: 2, ClientID: "tab"}
	var appliedWrites []string
	writeEntered := make(chan struct{})
	finishOlder := make(chan struct{})
	olderDone := make(chan gateResult, 1)
	go func() {
		applied, err := gate.apply(providerID, older, func() error {
			close(writeEntered)
			<-finishOlder
			appliedWrites = append(appliedWrites, "older")
			return nil
		})
		olderDone <- gateResult{applied: applied, err: err}
	}()
	<-writeEntered
	newerDone := make(chan gateResult, 1)
	go func() {
		applied, err := gate.apply(providerID, newer, func() error {
			appliedWrites = append(appliedWrites, "newer")
			return nil
		})
		newerDone <- gateResult{applied: applied, err: err}
	}()
	close(finishOlder)
	if result := <-olderDone; result.err != nil || !result.applied {
		t.Fatalf("apply active older = (%t, %v), want (true, nil)", result.applied, result.err)
	}
	if result := <-newerDone; result.err != nil || !result.applied {
		t.Fatalf("apply newer = (%t, %v), want (true, nil)", result.applied, result.err)
	}
	if !reflect.DeepEqual(appliedWrites, []string{"older", "newer"}) {
		t.Fatalf("applied writes = %v, want [older newer]", appliedWrites)
	}
}

func TestKeyOrderWriteGateRetriesFailedRevisionAndKeepsOlderWritesSuppressed(t *testing.T) {
	var gate keyOrderWriteGate
	const providerID int64 = 7
	older := keyOrderRevision{Sequence: 1, ClientID: "tab"}
	newer := keyOrderRevision{Sequence: 2, ClientID: "tab"}

	if applied, err := gate.apply(providerID, newer, func() error { return errors.New("temporary failure") }); err == nil || !applied {
		t.Fatalf("failed write = (%t, %v), want (true, error)", applied, err)
	}
	if applied, err := gate.apply(providerID, older, func() error {
		t.Fatal("older write ran after the newer write failed")
		return nil
	}); err != nil || applied {
		t.Fatalf("apply older = (%t, %v), want (false, nil)", applied, err)
	}
	if applied, err := gate.apply(providerID, newer, func() error { return nil }); err != nil || !applied {
		t.Fatalf("retry newer = (%t, %v), want (true, nil)", applied, err)
	}
	if applied, err := gate.apply(providerID, newer, func() error {
		t.Fatal("successful duplicate revision ran twice")
		return nil
	}); err != nil || applied {
		t.Fatalf("duplicate newer = (%t, %v), want (false, nil)", applied, err)
	}
}
