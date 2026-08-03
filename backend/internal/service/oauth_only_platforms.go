package service

func isOAuthOnlyRestrictedPlatform(platform string) bool {
	switch platform {
	case PlatformOpenAI, PlatformAntigravity, PlatformAnthropic, PlatformGemini, PlatformKiro, PlatformGrok, PlatformZed:
		return true
	default:
		return false
	}
}
