package sleep

import (
	"strings"
	"testing"
)

func TestLidScalarTransport(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var scalar uint64
		var closed uint32
		transport := lidTransport{
			open: func() uint32 { return 71 },
			call: func(connection, selector uint32, input uint64, count uint32) uint32 {
				if connection != 71 || selector != 12 || count != 1 {
					t.Fatalf("connection=%d selector=%d count=%d", connection, selector, count)
				}
				scalar = input
				return 0
			},
			close: func(connection uint32) uint32 { closed = connection; return 0 },
		}
		if applied, err := transport.set(enabled); err != nil || !applied {
			t.Fatalf("applied=%v error=%v", applied, err)
		}
		want := uint64(0)
		if enabled {
			want = 1
		}
		if scalar != want || closed != 71 {
			t.Fatalf("scalar=%v close=%d", scalar, closed)
		}
	}
}

func TestLidScalarTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		connection, call, close uint32
		want                    string
		applied                 bool
	}{
		{"open", 0, 0, 0, "IOPMFindPowerManagement", false},
		{"selector", 71, 0xe00002c1, 0, "0xe00002c1", false},
		{"close", 71, 0, 0xe00002bc, "0xe00002bc", true},
		{"selector-and-close", 71, 0xe00002c1, 0xe00002bc, "0xe00002c1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, closes := 0, 0
			transport := lidTransport{
				open:  func() uint32 { return tc.connection },
				call:  func(uint32, uint32, uint64, uint32) uint32 { calls++; return tc.call },
				close: func(uint32) uint32 { closes++; return tc.close },
			}
			applied, err := transport.set(true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
			if applied != tc.applied {
				t.Fatalf("applied=%v, want %v", applied, tc.applied)
			}
			if tc.connection == 0 {
				if calls != 0 || closes != 0 {
					t.Fatal("used unopened connection")
				}
			} else if calls != 1 || closes != 1 {
				t.Fatalf("calls=%d closes=%d", calls, closes)
			}
		})
	}
}
