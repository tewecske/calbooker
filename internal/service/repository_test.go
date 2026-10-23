package service

import (
	"errors"
	"testing"
)

func TestClassifyStaleWrite(t *testing.T) {
	if err := ClassifyStaleWrite(false); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("ClassifyStaleWrite(false) = %v, want ErrOptimisticLockConflict", err)
	}
	if err := ClassifyStaleWrite(true); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("ClassifyStaleWrite(true) = %v, want ErrRecordNotFound", err)
	}
}

func TestIsWriteConflict(t *testing.T) {
	if IsWriteConflict(nil) {
		t.Fatal("IsWriteConflict(nil) = true, want false")
	}
	if !IsWriteConflict(ErrOptimisticLockConflict) {
		t.Fatal("IsWriteConflict(ErrOptimisticLockConflict) = false, want true")
	}
	if !IsWriteConflict(ErrRecordNotFound) {
		t.Fatal("IsWriteConflict(ErrRecordNotFound) = false, want true")
	}
	if !IsWriteConflict(errors.Join(errors.New("outer"), ErrOptimisticLockConflict)) {
		t.Fatal("IsWriteConflict(wrapped) = false, want true")
	}
}
