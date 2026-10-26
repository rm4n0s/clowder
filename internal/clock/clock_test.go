package clock

import (
	"sync"
	"testing"
	"time"
)

func TestReal(t *testing.T) {
	before := time.Now()
	got := Real().Now()
	after := time.Now()
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestFake(t *testing.T) {
	start := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	plus2 := time.FixedZone("plus2", 2*60*60)
	tests := []struct {
		name string
		do   func(f *Fake)
		want time.Time
	}{
		{"start", func(*Fake) {}, start},
		{"advance", func(f *Fake) { f.Advance(90 * time.Minute) }, start.Add(90 * time.Minute)},
		{"advance twice", func(f *Fake) { f.Advance(time.Hour); f.Advance(time.Hour) }, start.Add(2 * time.Hour)},
		{"advance back", func(f *Fake) { f.Advance(-time.Hour) }, start.Add(-time.Hour)},
		{"set", func(f *Fake) { f.Set(start.AddDate(1, 0, 0)) }, start.AddDate(1, 0, 0)},
		{"set then advance", func(f *Fake) { f.Set(start.Add(time.Hour)); f.Advance(time.Minute) }, start.Add(61 * time.Minute)},
		{"set in another zone", func(f *Fake) { f.Set(time.Date(2026, 1, 1, 2, 0, 0, 0, plus2)) }, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFake(start)
			tt.do(f)
			got := f.Now()
			if !got.Equal(tt.want) {
				t.Errorf("Now() = %v, want %v", got, tt.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("location = %v, want UTC", got.Location())
			}
		})
	}
}

func TestNewFakeConvertsToUTC(t *testing.T) {
	zone := time.FixedZone("plus3", 3*60*60)
	f := NewFake(time.Date(2026, 1, 1, 3, 0, 0, 0, zone))
	if got := f.Now().Location(); got != time.UTC {
		t.Errorf("location = %v, want UTC", got)
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := NewFake(time.Unix(0, 0))
	const workers, steps = 8, 100
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range steps {
				f.Advance(time.Second)
				_ = f.Now()
			}
		}()
	}
	wg.Wait()
	want := time.Unix(0, 0).UTC().Add(workers * steps * time.Second)
	if got := f.Now(); !got.Equal(want) {
		t.Errorf("Now() = %v, want %v", got, want)
	}
}
