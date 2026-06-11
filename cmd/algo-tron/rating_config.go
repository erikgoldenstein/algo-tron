package main

const (
	eloKFactor = 16

	// TrueSkill parameters (Herbrich, Minka, Graepel 2007). The paper's defaults
	// are mu0=25, sigma0=25/3, beta=sigma0/2, tau=sigma0/100; we scale by 10x so
	// displayed ratings sit in the hundreds with sigma in the tens.
	tsMu0    = 250.0
	tsSigma0 = 250.0 / 3.0
	tsBeta   = tsSigma0 / 2.0
	tsTau    = tsSigma0 / 100.0
)
