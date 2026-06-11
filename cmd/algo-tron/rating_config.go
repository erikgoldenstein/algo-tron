package main

const (
	eloKFactor = 16

	// TrueSkill parameters (Herbrich, Minka, Graepel 2007). The paper's defaults
	// are mu0=25, sigma0=25/3, beta=sigma0/2, tau=sigma0/100; we scale by 10x so
	// displayed ratings sit in the hundreds with sigma in the tens. beta is
	// 4x the paper's sigma0/2: more assumed performance noise per game means
	// each result moves ratings less, so they converge slower.
	tsMu0    = 250.0
	tsSigma0 = 250.0 / 3.0
	tsBeta   = 2 * tsSigma0
	tsTau    = tsSigma0 / 100.0
)
