//go:build !linux

package sample

import "ltm/internal/storage"

// New returns a no-op sampler on non-Linux platforms. The daemon checks
// Supported() and skips the sample loop, so these methods are never called in
// practice; they exist to keep the package building everywhere.
func New() Sampler { return stub{} }

// Supported reports whether sampling works on this platform.
func Supported() bool { return false }

type stub struct{}

func (stub) System() (storage.SystemSample, error) { return storage.SystemSample{}, ErrUnsupported }

func (stub) Processes() ([]storage.ProcessSample, error) { return nil, ErrUnsupported }
