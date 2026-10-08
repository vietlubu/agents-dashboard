//go:build darwin

package sleep

import (
	"reflect"
	"strings"
	"testing"
)

// capturedAssertions is a real `pmset -g assertions` report taken while audio was playing
// and the dashboard held its own caffeinate assertion (pid 78180). Every trap that matters
// is in here: our own child, powerd's system-wide aggregate, and the HID tickle.
const capturedAssertions = `2026-10-08 14:35:20 +0700 
Assertion status system-wide:
   BackgroundTask                 0
   ApplePushServiceTask           0
   UserIsActive                   1
   PreventUserIdleDisplaySleep    1
   PreventSystemSleep             0
   ExternalMedia                  0
   PreventUserIdleSystemSleep     1
   NetworkClientActive            0
Listed by owning process:
   pid 5999(bun): [0x000281eb000196dd] 00:08:54 PreventUserIdleSystemSleep named: "omp agent session"  
   pid 5999(bun): [0x0002806a00019642] 00:15:19 PreventUserIdleSystemSleep named: "omp agent session"  
   pid 416(coreaudiod): [0x000274ca000192cf] 01:04:55 PreventUserIdleSystemSleep named: "com.apple.audio.CC-14-BC-3A-19-A6:output.context.preventuseridlesleep"  
	Created for PID: 762. 
	Resources: audio-out CC-14-BC-3A-19-A6:output 
   pid 380(WindowServer): [0x0002791500099456] 00:00:00 UserIsActive named: "com.apple.iohideventsystem.queue.tickle serviceID:10003f3b2 service:AppleMultitouchDevice product:Apple Internal Keyboard / Trackpad eventType:11"  
	Timeout will fire in 300 secs Action=TimeoutActionRelease
   pid 33794(caffeinate): [0x000280b80001964d] 00:14:01 PreventUserIdleSystemSleep named: "caffeinate command-line tool"  
	Details: caffeinate asserting on behalf of Process ID 78180
	Created for PID: 78180. 
	Localized=THE CAFFEINATE TOOL IS PREVENTING SLEEP.
   pid 33794(caffeinate): [0x000280b80005964e] 00:14:01 PreventUserIdleDisplaySleep named: "caffeinate command-line tool"  
	Details: caffeinate asserting on behalf of Process ID 78180
	Created for PID: 78180. 
	Localized=THE CAFFEINATE TOOL IS PREVENTING SLEEP.
   pid 326(powerd): [0x0002754600019380] 01:02:51 PreventUserIdleSystemSleep named: "Powerd - Prevent sleep while display is on"  
No kernel assertions.
`

const idleAssertions = `2026-10-08 16:20:00 +0700 
Assertion status system-wide:
   PreventUserIdleDisplaySleep    1
   ExternalMedia                  0
   PreventUserIdleSystemSleep     1
Listed by owning process:
   pid 33794(caffeinate): [0x000280b80001964d] 00:14:01 PreventUserIdleSystemSleep named: "caffeinate command-line tool"  
	Created for PID: 78180. 
   pid 33794(caffeinate): [0x000280b80005964e] 00:14:01 PreventUserIdleDisplaySleep named: "caffeinate command-line tool"  
	Created for PID: 78180. 
   pid 326(powerd): [0x0002754600019380] 01:02:51 PreventUserIdleSystemSleep named: "Powerd - Prevent sleep while display is on"  
   pid 380(WindowServer): [0x0002791500099456] 00:00:00 UserIsActive named: "com.apple.iohideventsystem.queue.tickle serviceID:10003f3b2"  
`

func TestParseAssertions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		report   string
		ownPID   int
		want     Media
		wantPlay bool
	}{
		{
			name:     "audio playback",
			report:   capturedAssertions,
			ownPID:   78180,
			want:     Media{Playing: true, Sources: []string{"audio output"}},
			wantPlay: true,
		},
		{
			// The regression that caused auto-sleep to interrupt videos: only our own
			// assertion and powerd's aggregate remain, so nothing is playing.
			name:   "own caffeinate and powerd only",
			report: idleAssertions,
			ownPID: 78180,
		},
		{
			name: "browser video wake lock",
			report: `Listed by owning process:
   pid 900(Google Chrome): [0x0001] 00:00:10 PreventUserIdleDisplaySleep named: "video wake lock"  
`,
			want:     Media{Playing: true, Sources: []string{"Google Chrome: video"}},
			wantPlay: true,
		},
		{
			name: "media player assertion",
			report: `Listed by owning process:
   pid 500(Music): [0x0002] 00:01:00 PreventUserIdleSystemSleep named: "com.apple.Music.playback"  
`,
			want:     Media{Playing: true, Sources: []string{"Music"}},
			wantPlay: true,
		},
		{
			// Media is the only signal that defers sleep. An app holding the machine awake
			// for a download, a backup or a build is not playback.
			name: "non-media keep-awake assertion",
			report: `Listed by owning process:
   pid 777(Docker): [0x0003] 00:30:00 PreventUserIdleSystemSleep named: "docker desktop"  
   pid 778(Time Machine): [0x0004] 00:30:00 PreventUserIdleSystemSleep named: "backup in progress"  
`,
		},
		{
			name: "external media device",
			report: `Assertion status system-wide:
   ExternalMedia                  1
Listed by owning process:
`,
			want:     Media{Playing: true, Sources: []string{"external media"}},
			wantPlay: true,
		},
		{
			// Two audio assertions are one reason to stay awake, not two.
			name: "duplicate sources are collapsed",
			report: `Listed by owning process:
   pid 416(coreaudiod): [0x0005] 00:01:00 PreventUserIdleSystemSleep named: "com.apple.audio.A:output.context.preventuseridlesleep"  
   pid 416(coreaudiod): [0x0006] 00:01:00 PreventUserIdleDisplaySleep named: "com.apple.audio.B:output.context.preventuseridlesleep"  
`,
			want:     Media{Playing: true, Sources: []string{"audio output"}},
			wantPlay: true,
		},
		{
			// Attribution matters: the created-for line belongs to the entry above it, so a
			// real playback assertion after our own child must survive.
			name: "created-for does not leak to the next entry",
			report: `Listed by owning process:
   pid 33794(caffeinate): [0x0007] 00:14:01 PreventUserIdleSystemSleep named: "caffeinate command-line tool"  
	Created for PID: 78180. 
   pid 416(coreaudiod): [0x0008] 00:01:00 PreventUserIdleSystemSleep named: "com.apple.audio.C:output.context.preventuseridlesleep"  
`,
			ownPID:   78180,
			want:     Media{Playing: true, Sources: []string{"audio output"}},
			wantPlay: true,
		},
		{
			name: "empty report",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAssertions(tc.report, tc.ownPID)
			if got.Playing != tc.wantPlay {
				t.Fatalf("Playing = %v, want %v (sources %v)", got.Playing, tc.wantPlay, got.Sources)
			}
			if len(tc.want.Sources) == 0 {
				if len(got.Sources) != 0 {
					t.Fatalf("Sources = %v, want none", got.Sources)
				}
				return
			}
			if !reflect.DeepEqual(got.Sources, tc.want.Sources) {
				t.Errorf("Sources = %v, want %v", got.Sources, tc.want.Sources)
			}
		})
	}
}

// The captured report must keep classifying as playback even when the dashboard is not the
// owner of the caffeinate child, because a user-run caffeinate for another app is a
// deliberate keep-awake request, not playback.
func TestParseAssertionsIgnoresForeignCaffeinate(t *testing.T) {
	got := parseAssertions(idleAssertions, 999)
	if got.Playing {
		t.Fatalf("foreign caffeinate counted as playback: %v", got.Sources)
	}
	if strings.Contains(strings.Join(got.Sources, ","), "caffeinate") {
		t.Fatalf("caffeinate leaked into sources: %v", got.Sources)
	}
}
