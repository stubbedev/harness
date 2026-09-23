package config

// ResolverFunc returns the store's resolver as a plain function, or nil
// when none is configured. Packages that expand configured paths take
// this shape so they need not depend on VariableResolver.
func (s *ConfigStore) ResolverFunc() func(string) (string, error) {
	if r := s.Resolver(); r != nil {
		return r.ResolveValue
	}
	return nil
}
