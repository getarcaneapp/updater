package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.getarcane.app/updater/pkg/utils"
)

const (
	defaultRegistryHost                   = "registry-1.docker.io"
	daemonProxyConnectIndicatorInternal   = "proxy" + "connect"
	registryRateLimitHeaderSourceInternal = "rate" + "limit"
)

// Credentials contains registry credentials used for manifest requests.
type Credentials struct {
	Username string
	Token    string
}

// RateLimitInfo contains pull quota information returned by registry headers.
type RateLimitInfo struct {
	Limit         *int   `json:"limit,omitempty"`
	Remaining     *int   `json:"remaining,omitempty"`
	Used          *int   `json:"used,omitempty"`
	WindowSeconds *int   `json:"windowSeconds,omitempty"`
	Source        string `json:"source,omitempty"`
}

// NewRegistryHTTPClient creates the default registry HTTP client.
func NewRegistryHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// IsFallbackEligibleDaemonError reports whether a daemon registry error should use direct HTTP fallback.
func IsFallbackEligibleDaemonError(err error) bool {
	if err == nil {
		return false
	}

	errLower := strings.ToLower(err.Error())
	for _, blocked := range []string{
		"unauthorized", "authentication required", "no basic auth credentials", "access denied",
		"incorrect username or password", "status: 401", "status 401", "x509", "certificate", "tls",
	} {
		if strings.Contains(errLower, blocked) {
			return false
		}
	}

	for _, indicator := range []string{
		"not found", " 404 ", "status: 404", "status 404", "403 forbidden", "status: 403",
		"status 403", "administrative rules", "not implemented", "unsupported",
		"distribution disabled", "distribution api", daemonProxyConnectIndicatorInternal,
	} {
		if strings.Contains(errLower, indicator) {
			return true
		}
	}
	return false
}

// FetchRegistryRateLimit fetches registry pull rate-limit information.
func FetchRegistryRateLimit(ctx context.Context, registryHost, repository, tag string, credential *Credentials, httpClient *http.Client) (*RateLimitInfo, error) {
	if httpClient == nil {
		httpClient = NewRegistryHTTPClient()
	}

	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	header, err := authorizedManifestHeadersInternal(requestCtx, httpClient, http.MethodHead, registryHost, repository, tag, credential)
	if err != nil {
		return nil, err
	}
	return extractRateLimitFromHeadersInternal(header)
}

// FetchDigest fetches the manifest digest for a registry image reference.
func FetchDigest(ctx context.Context, registryHost, repository, tag string, credential *Credentials, httpClient *http.Client) (string, error) {
	if httpClient == nil {
		httpClient = NewRegistryHTTPClient()
	}

	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	header, err := authorizedManifestHeadersInternal(requestCtx, httpClient, http.MethodGet, registryHost, repository, tag, credential)
	if err != nil {
		return "", err
	}

	digest := extractDigestFromHeadersInternal(header)
	if digest == "" {
		return "", errors.New("no digest header found in response")
	}
	return digest, nil
}

func authorizedManifestHeadersInternal(ctx context.Context, httpClient *http.Client, method, registryHost, repository, tag string, credential *Credentials) (http.Header, error) {
	resp, err := manifestRequestInternal(ctx, httpClient, method, registryHost, repository, tag, basicAuthHeaderForCredentialInternal(credential))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		_ = resp.Body.Close()
		if challenge == "" {
			return nil, fmt.Errorf("manifest request failed with status: %d", resp.StatusCode)
		}
		realm, service := parseWWWAuthInternal(challenge)
		if realm == "" {
			return nil, errors.New("no auth realm found")
		}
		if err := validateAuthRealmInternal(registryHost, realm); err != nil {
			return nil, err
		}
		token, err := fetchRegistryTokenInternal(ctx, httpClient, realm, service, repository, credential)
		if err != nil {
			return nil, err
		}
		resp, err = manifestRequestInternal(ctx, httpClient, method, registryHost, repository, tag, token)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("authenticated manifest request failed with status: %d", resp.StatusCode)
		}
		return resp.Header, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest request failed with status: %d", resp.StatusCode)
	}
	return resp.Header, nil
}

func manifestRequestInternal(ctx context.Context, httpClient *http.Client, method, registryHost, repository, tag, authHeader string) (*http.Response, error) {
	registryHost = utils.NormalizeRegistryForComparison(registryHost)
	if registryHost == "docker.io" {
		registryHost = defaultRegistryHost
	}

	manifestURL := url.URL{
		Scheme: "https",
		Host:   registryHost,
		Path:   "/v2/" + strings.Trim(repository, "/") + "/manifests/" + strings.TrimSpace(tag),
	}
	req, err := http.NewRequestWithContext(ctx, method, manifestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return httpClient.Do(req)
}

func fetchRegistryTokenInternal(ctx context.Context, httpClient *http.Client, authURL, service, repository string, credential *Credentials) (string, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	q.Set("scope", "repository:"+repository+":pull")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if credential != nil && strings.TrimSpace(credential.Username) != "" && strings.TrimSpace(credential.Token) != "" {
		req.SetBasicAuth(strings.TrimSpace(credential.Username), strings.TrimSpace(credential.Token))
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request failed with status: %d", resp.StatusCode)
	}

	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}
	token := strings.TrimSpace(tokenResp.Token)
	if token == "" {
		token = strings.TrimSpace(tokenResp.AccessToken)
	}
	if token == "" {
		return "", errors.New("token response did not contain a token")
	}
	return "Bearer " + token, nil
}

func parseWWWAuthInternal(challenge string) (realm, service string) {
	challenge = strings.TrimSpace(challenge)
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", ""
	}
	params := strings.TrimSpace(challenge[len("Bearer "):])
	for _, part := range splitAuthParamsInternal(params) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			realm = value
		case "service":
			service = value
		}
	}
	return realm, service
}

func validateAuthRealmInternal(_ string, realm string) error {
	u, err := url.Parse(realm)
	if err != nil {
		return err
	}
	if strings.ToLower(u.Scheme) != "https" {
		return fmt.Errorf("registry auth realm must use https: %s", realm)
	}
	if strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("invalid registry auth realm host: %s", realm)
	}
	return nil
}

func splitAuthParamsInternal(params string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	for _, r := range params {
		switch r {
		case '"':
			inQuote = !inQuote
			current.WriteRune(r)
		case ',':
			if inQuote {
				current.WriteRune(r)
				continue
			}
			parts = append(parts, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, strings.TrimSpace(current.String()))
	}
	return parts
}

func extractDigestFromHeadersInternal(header http.Header) string {
	for _, key := range []string{"Docker-Content-Digest", "OCI-Content-Digest"} {
		if value := strings.TrimSpace(header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func extractRateLimitFromHeadersInternal(header http.Header) (*RateLimitInfo, error) {
	info := &RateLimitInfo{}
	if limit, window := parseRateLimitHeaderInternal(header.Get("RateLimit-Limit")); limit != nil {
		info.Limit = limit
		info.WindowSeconds = window
		info.Source = registryRateLimitHeaderSourceInternal
	}
	if remaining, window := parseRateLimitHeaderInternal(header.Get("RateLimit-Remaining")); remaining != nil {
		info.Remaining = remaining
		if info.WindowSeconds == nil {
			info.WindowSeconds = window
		}
		info.Source = registryRateLimitHeaderSourceInternal
	}
	if used, err := strconv.Atoi(strings.TrimSpace(header.Get("Docker-RateLimit-Used"))); err == nil {
		info.Used = &used
		if info.Source == "" {
			info.Source = "docker"
		}
	}
	if info.Limit == nil && info.Remaining == nil && info.Used == nil {
		return nil, errors.New("no registry rate limit headers found")
	}
	return info, nil
}

func parseRateLimitHeaderInternal(value string) (*int, *int) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	first, rest, _ := strings.Cut(value, ";")
	n, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return nil, nil
	}

	var window *int
	for part := range strings.SplitSeq(rest, ";") {
		key, rawValue, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.ToLower(key) != "w" {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(rawValue))
		if err == nil {
			window = &parsed
		}
	}
	return &n, window
}

func basicAuthHeaderForCredentialInternal(credential *Credentials) string {
	if credential == nil || strings.TrimSpace(credential.Username) == "" || strings.TrimSpace(credential.Token) == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(credential.Username)+":"+strings.TrimSpace(credential.Token)))
}
