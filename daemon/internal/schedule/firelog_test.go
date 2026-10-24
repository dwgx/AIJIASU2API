package schedule

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testNow() time.Time {
	return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
}

func TestScheduleFireLogReserveIsExclusive(t *testing.T) {
	log, err := OpenFireLog(filepath.Join(t.TempDir(), "schedule-fire.log"), func() time.Time { return testNow() })
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()

	var wg sync.WaitGroup
	var mu sync.Mutex
	reserved := 0
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := log.Reserve("sch-001@1792695600", "sch-001", 1792695600, "run-0001", "schedule:sch-001", testNow())
			if err != nil {
				t.Errorf("Reserve: %v", err)
				return
			}
			if ok {
				mu.Lock()
				reserved++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if reserved != 1 {
		t.Fatalf("reserved %d times, want exactly 1", reserved)
	}
}

func TestScheduleFireLogCrashReplayBecomesUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule-fire.log")
	log, err := OpenFireLog(path, func() time.Time { return testNow() })
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	if ok, err := log.Reserve("sch-001@100", "sch-001", 100, "run-0001", "schedule:sch-001", testNow()); err != nil || !ok {
		t.Fatalf("Reserve = %v, %v", ok, err)
	}
	// Simulate a crash: close without Finish.
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenFireLog(path, func() time.Time { return testNow().Add(time.Minute) })
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	unknown := reopened.TakeUnknown()
	if len(unknown) != 1 || unknown[0].State != "unknown" || unknown[0].RunID != "run-0001" {
		t.Fatalf("unknown = %+v", unknown)
	}
	if ok, err := reopened.Reserve("sch-001@100", "sch-001", 100, "run-0002", "schedule:sch-001", testNow()); err != nil || ok {
		t.Fatalf("reserve after replay = %v, %v; want duplicate", ok, err)
	}
	// The unknown state is now terminal, so a second open does not re-audit.
	_ = reopened.Close()
	again, err := OpenFireLog(path, func() time.Time { return testNow().Add(2 * time.Minute) })
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer again.Close()
	if unknown := again.TakeUnknown(); len(unknown) != 0 {
		t.Fatalf("unknown replayed twice: %+v", unknown)
	}
}

func TestScheduleFireLogFinishAndCompact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule-fire.log")
	log, err := OpenFireLog(path, func() time.Time { return testNow() })
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	if ok, err := log.Reserve("sch-001@100", "sch-001", 100, "run-0001", "schedule:sch-001", testNow()); err != nil || !ok {
		t.Fatalf("Reserve = %v, %v", ok, err)
	}
	if err := log.Finish("sch-001@100", "succeeded", testNow().Add(time.Second)); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	records := log.Records()
	if len(records) != 1 || records[0].State != "succeeded" {
		t.Fatalf("records = %+v", records)
	}
	if err := log.Compact(records); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	_ = log.Close()

	reopened, err := OpenFireLog(path, func() time.Time { return testNow().Add(time.Minute) })
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got := reopened.Records()
	if len(got) != 1 || got[0].State != "succeeded" {
		t.Fatalf("after compact = %+v", got)
	}
	if len(reopened.TakeUnknown()) != 0 {
		t.Fatal("compacted terminal record became unknown")
	}
}

func TestScheduleFireLogTornLastLineIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule-fire.log")
	log, err := OpenFireLog(path, func() time.Time { return testNow() })
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	if _, err := log.Reserve("sch-001@100", "sch-001", 100, "run-0001", "schedule:sch-001", testNow()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_ = log.Close()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := file.WriteString(`{"op":"reserve","key":"torn`); err != nil {
		t.Fatalf("write torn line: %v", err)
	}
	_ = file.Close()
	reopened, err := OpenFireLog(path, func() time.Time { return testNow() })
	if err != nil {
		t.Fatalf("reopen with torn line: %v", err)
	}
	defer reopened.Close()
	if len(reopened.Records()) != 1 {
		t.Fatalf("records = %+v", reopened.Records())
	}
}
