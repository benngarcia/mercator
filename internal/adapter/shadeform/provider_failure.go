package shadeform

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/benngarcia/mercator/internal/adapter"
)

const maxProviderResponseBodyBytes = 4 * 1024

func (c *client) createFailure(request createRequest, result httpResult) *adapter.ProviderFailure {
	body, truncated := c.sanitizedResponseBody(request, result)
	failure := &adapter.ProviderFailure{
		Kind:              adapter.ProviderFailureInternal,
		Status:            result.status,
		ProviderCode:      providerCode(result.body),
		Retryable:         true,
		SideEffect:        adapter.SideEffectIndeterminate,
		ResponseBody:      body,
		RetryCount:        result.retryCount,
		ResponseTruncated: truncated,
	}
	if result.status == 0 {
		failure.Kind = adapter.ProviderFailureTransport
		return failure
	}
	switch {
	case result.status == http.StatusConflict && strings.EqualFold(failure.ProviderCode, "OUT_OF_STOCK"):
		failure.Kind = adapter.ProviderFailureCapacityUnavailable
		failure.SideEffect = adapter.SideEffectNone
	case result.status == http.StatusUnauthorized || result.status == http.StatusForbidden:
		failure.Kind = adapter.ProviderFailureAuthentication
		failure.Retryable = false
		failure.SideEffect = adapter.SideEffectNone
	case result.status == http.StatusTooManyRequests:
		failure.Kind = adapter.ProviderFailureRateLimited
		failure.SideEffect = adapter.SideEffectNone
	case result.status >= 400 && result.status < 500:
		failure.Kind = adapter.ProviderFailureInvalidRequest
		failure.Retryable = false
		failure.SideEffect = adapter.SideEffectNone
	}
	return failure
}

func (c *client) invalidCreateResponse(request createRequest, result httpResult) *adapter.ProviderFailure {
	body, truncated := c.sanitizedResponseBody(request, result)
	return &adapter.ProviderFailure{
		Kind:              adapter.ProviderFailureInternal,
		Status:            result.status,
		ProviderCode:      providerCode(result.body),
		Retryable:         true,
		SideEffect:        adapter.SideEffectIndeterminate,
		ResponseBody:      body,
		RetryCount:        result.retryCount,
		ResponseTruncated: truncated,
	}
}

func (c *client) sanitizedResponseBody(request createRequest, result httpResult) (string, bool) {
	secrets := requestSecrets(c.apiKey, request)
	var value any
	var sanitized string
	if json.Unmarshal(result.body, &value) == nil {
		encoded, err := json.Marshal(redactResponseValue(value, secrets))
		if err == nil {
			sanitized = string(encoded)
		}
	}
	if sanitized == "" && len(result.body) > 0 {
		sanitized = redactResponseString(strings.ToValidUTF8(string(result.body), "�"), secrets)
	}
	if len(sanitized) <= maxProviderResponseBodyBytes {
		return sanitized, result.bodyTruncated
	}
	return strings.ToValidUTF8(sanitized[:maxProviderResponseBodyBytes], "�"), true
}

func requestSecrets(apiKey string, request createRequest) []string {
	secrets := []string{apiKey}
	if launch := request.LaunchConfiguration; launch != nil && launch.DockerConfiguration != nil {
		docker := launch.DockerConfiguration
		if docker.RegistryCredentials != nil {
			secrets = append(secrets, docker.RegistryCredentials.Username, docker.RegistryCredentials.Password)
		}
		for _, env := range docker.Envs {
			secrets = append(secrets, env.Value)
		}
	}
	if encoded, err := json.Marshal(request); err == nil {
		secrets = append(secrets, string(encoded))
	}
	filtered := secrets[:0]
	for _, secret := range secrets {
		if secret != "" {
			filtered = append(filtered, secret)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return len(filtered[i]) > len(filtered[j]) })
	return filtered
}

func redactResponseValue(value any, secrets []string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveResponseKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactResponseValue(child, secrets)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactResponseValue(child, secrets)
		}
		return out
	case string:
		return redactResponseString(typed, secrets)
	default:
		return value
	}
}

func redactResponseString(value string, secrets []string) string {
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func sensitiveResponseKey(key string) bool {
	var normalized strings.Builder
	for _, r := range strings.ToLower(key) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	switch normalized.String() {
	case "apikey", "xapikey", "authorization", "headers", "password", "token", "secret",
		"credential", "credentials", "registrycredentials", "env", "envs", "environment",
		"launchconfiguration", "dockerconfiguration", "request", "requestbody", "payload":
		return true
	default:
		return false
	}
}

func providerCode(body []byte) string {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return ""
	}
	return findProviderCode(value)
}

func findProviderCode(value any) string {
	typed, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"code", "error_code", "errorCode"} {
		if code, ok := typed[key].(string); ok && safeProviderCode(code) {
			return code
		}
	}
	if nested, ok := typed["error"]; ok {
		if code, ok := nested.(string); ok && safeProviderCode(code) {
			return code
		}
		if code := findProviderCode(nested); code != "" {
			return code
		}
	}
	return ""
}

func safeProviderCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, r := range code {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}
