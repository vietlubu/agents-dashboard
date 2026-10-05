package sleep

// Platform bundles the operating-system pieces the controller needs. Each OS contributes
// its own defaults; a build with no supported platform gets no-ops.
type Platform struct {
	Inhibitor Inhibitor
	Sleeper   Sleeper
	Idler     Idler
	Processes ProcessLister
	Supported bool
}

// DefaultPlatform returns the platform pieces for the current build.
func DefaultPlatform() Platform {
	return Platform{
		Inhibitor: defaultInhibitor(),
		Sleeper:   defaultSleeper(),
		Idler:     defaultIdler(),
		Processes: DefaultProcessLister(),
		Supported: PlatformSupported(),
	}
}
