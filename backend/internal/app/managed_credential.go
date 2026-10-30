package app

// resolveProviderSecrets keeps legacy task rows readable while providers use
// credentials from their local channel configuration.
func (s *Service) resolveProviderSecrets(input map[string]any) (map[string]any, error) {
	return input, nil
}

func (s *Service) ResolveCustomRelayAPIKey(targetURL, incoming string) (string, error) {
	_ = targetURL
	return incoming, nil
}
