// Package service is modb's internal implementation package. Other modules
// must not import it directly.
package service

// Service is a concrete implementation type.
type Service struct{}

// Do implements the operation.
func (s *Service) Do() error { return nil }
