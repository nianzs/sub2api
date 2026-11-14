package service

// SanitizeStoredCredentials strips secrets that must never be persisted on the
// account credentials map after conversion to OAuth tokens (Grok Web SSO / password).
// Call from admin create/update/import/apply-oauth paths.
//
// Cookie is always stripped: for Grok/OpenAI it is session-jar residue left over
// after it was exchanged for OAuth tokens, and keeping it next to those tokens is
// pure liability.
//
// Bulk paths pass an empty platform label. Stripping cookie there is intentional and harmless: bulk credentials are a JSONB merge delta, so dropping the
// key means "leave the stored cookie alone" rather than clearing it.
func SanitizeStoredCredentials(platform string, creds map[string]any) map[string]any {
	if creds == nil {
		return nil
	}
	for _, key := range []string{
		"password", "sso_token", "sso", "sso-rw", "clearTextPassword",
	} {
		delete(creds, key)
	}
	delete(creds, "cookie")
	return creds
}
