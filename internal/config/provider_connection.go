package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2/google"

	"github.com/stubbedev/harness/internal/catalog"
)

const (
	ProviderParamAPIVersion = "apiVersion"
	ProviderParamProject    = "project"
	ProviderParamLocation   = "location"
	ProviderParamRegion     = "region"
	ProviderParamProfile    = "profile"
)

const (
	connectionTestTimeout = 10 * time.Second
	connectionErrorBody   = 4096
	connectionErrorMaxLen = 300
	azureTestAPIVersion   = "2024-10-21"
	vertexScope           = "https://www.googleapis.com/auth/cloud-platform"
)

var errMissingParam = errors.New("missing required setting")

type ConnectionError struct {
	Provider catalog.InferenceProvider
	Status   int
	Message  string
}

func (e *ConnectionError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return fmt.Sprintf("%s rejected the credentials: %s", e.Provider, e.Message)
	case http.StatusForbidden:
		return fmt.Sprintf("%s accepted the credentials but denied access: %s", e.Provider, e.Message)
	case http.StatusNotFound:
		return fmt.Sprintf("%s could not find the endpoint, project or model: %s", e.Provider, e.Message)
	default:
		return fmt.Sprintf("%s answered %d: %s", e.Provider, e.Status, e.Message)
	}
}

func (e *ConnectionError) CredentialsRejected() bool {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	default:
		return false
	}
}

func (c *ProviderConfig) ResolveTypeParams(resolver VariableResolver) (map[string]string, error) {
	return c.resolveParamsFor(c.Type, resolver)
}

func (c *ProviderConfig) resolveParamsFor(providerType catalog.Type, resolver VariableResolver) (map[string]string, error) {
	params := make(map[string]string)
	set := func(key, template string, required bool) error {
		value, err := resolver.ResolveValue(template)
		if err != nil {
			return fmt.Errorf("resolving %s for provider %s: %w", key, c.ID, err)
		}
		if value == "" {
			if required {
				return fmt.Errorf("%w %s for provider %s", errMissingParam, key, c.ID)
			}
			return nil
		}
		params[key] = value
		return nil
	}
	var err error
	switch providerType {
	case catalog.TypeVertexAI:
		err = errors.Join(
			set(ProviderParamProject, cmp.Or(c.Project, "$VERTEXAI_PROJECT"), true),
			set(ProviderParamLocation, cmp.Or(c.Location, "$VERTEXAI_LOCATION"), true),
		)
	case catalog.TypeAzure:
		err = set(ProviderParamAPIVersion, cmp.Or(c.APIVersion, "$AZURE_OPENAI_API_VERSION"), false)
	case catalog.TypeBedrock:
		err = errors.Join(
			set(ProviderParamRegion, c.Region, false),
			set(ProviderParamProfile, c.Profile, false),
		)
	default:
	}
	if err != nil {
		return nil, err
	}
	return params, nil
}

func (c *ProviderConfig) TestConnection(ctx context.Context, resolver VariableResolver) error {
	apiKey, err := resolver.ResolveValue(c.APIKey)
	if err != nil {
		return fmt.Errorf("resolving API key for provider %s: %w", c.ID, err)
	}
	baseURL, err := resolver.ResolveValue(c.BaseURL)
	if err != nil {
		return fmt.Errorf("resolving base URL for provider %s: %w", c.ID, err)
	}
	extraHeaders, err := resolveHeaders(c.ExtraHeaders, resolver)
	if err != nil {
		return fmt.Errorf("resolving provider %s: %w", c.ID, err)
	}
	params, err := c.ResolveTypeParams(resolver)
	if err != nil {
		return err
	}

	switch c.ID {
	case catalog.InferenceProviderMiniMax, catalog.InferenceProviderMiniMaxChina:
		return nil
	case catalog.InferenceProviderAlibabaSingapore:
		if !strings.HasPrefix(apiKey, "sk-") {
			return fmt.Errorf("invalid API key format for provider %s", c.ID)
		}
		return nil
	default:
	}

	ctx, cancel := context.WithTimeout(ctx, connectionTestTimeout)
	defer cancel()

	var testURL string
	headers := make(map[string]string)
	switch c.Type {
	case catalog.TypeOpenAI, catalog.TypeOpenAICompat, catalog.TypeOpenRouter:
		baseURL = cmp.Or(baseURL, "https://api.openai.com/v1")
		switch c.ID {
		case catalog.InferenceProviderOpenRouter:
			testURL = baseURL + "/credits"
		case catalog.InferenceProviderOpenCodeGo, catalog.InferenceProviderOpenCodeZen:
			testURL = strings.TrimSuffix(baseURL, "/openai/v1") + "/v1/models"
		default:
			testURL = baseURL + "/models"
		}
		headers["Authorization"] = "Bearer " + apiKey
	case catalog.TypeAnthropic:
		baseURL = cmp.Or(baseURL, "https://api.anthropic.com/v1")
		switch c.ID {
		case catalog.InferenceKimiCoding:
			testURL = baseURL + "/v1/models"
		default:
			testURL = baseURL + "/models"
		}
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
	case catalog.TypeGoogle:
		baseURL = cmp.Or(baseURL, "https://generativelanguage.googleapis.com")
		testURL = baseURL + "/v1beta/models?key=" + url.QueryEscape(apiKey)
	case catalog.TypeAzure:
		if baseURL == "" {
			return fmt.Errorf("%w endpoint for provider %s", errMissingParam, c.ID)
		}
		version := cmp.Or(params[ProviderParamAPIVersion], azureTestAPIVersion)
		testURL = strings.TrimSuffix(baseURL, "/") + "/openai/models?api-version=" + url.QueryEscape(version)
		headers["api-key"] = apiKey
	case catalog.TypeVertexAI:
		token, err := vertexAccessToken(ctx)
		if err != nil {
			return fmt.Errorf("loading Google application default credentials: %w", err)
		}
		testURL = vertexTestURL(params[ProviderParamProject], params[ProviderParamLocation])
		headers["Authorization"] = "Bearer " + token
	case catalog.TypeBedrock:
		return testBedrock(ctx, apiKey, params)
	case catalog.TypeVercel:
		if strings.HasPrefix(apiKey, "vck_") {
			return nil
		}
		return errors.New("not a valid vercel api key")
	default:
		return fmt.Errorf("provider type %q has no connection test", c.Type)
	}
	return c.probe(ctx, testURL, headers, extraHeaders)
}

func (c *ProviderConfig) probe(ctx context.Context, testURL string, headers, extraHeaders map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach provider %s: %w", c.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if c.ID == catalog.InferenceProviderZAI && resp.StatusCode != http.StatusUnauthorized {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, connectionErrorBody))
	return &ConnectionError{Provider: c.ID, Status: resp.StatusCode, Message: apiErrorMessage(resp.StatusCode, body)}
}

func apiErrorMessage(status int, body []byte) string {
	if gjson.ValidBytes(body) {
		for _, path := range []string{"error.message", "message", "error_description", "detail", "error"} {
			if v := gjson.GetBytes(body, path); v.Type == gjson.String && v.String() != "" {
				return truncateMessage(v.String())
			}
		}
	}
	if text := strings.TrimSpace(string(body)); text != "" && !strings.HasPrefix(text, "<") {
		return truncateMessage(text)
	}
	return http.StatusText(status)
}

func truncateMessage(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= connectionErrorMaxLen {
		return s
	}
	return s[:connectionErrorMaxLen] + "..."
}

func vertexTestURL(project, location string) string {
	host := location + "-aiplatform.googleapis.com"
	if location == "global" {
		host = "aiplatform.googleapis.com"
	}
	return fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/endpoints?pageSize=1",
		host, url.PathEscape(project), url.PathEscape(location))
}

func vertexAccessToken(ctx context.Context) (string, error) {
	creds, err := google.FindDefaultCredentials(ctx, vertexScope)
	if err != nil {
		return "", err
	}
	token, err := creds.TokenSource.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func testBedrock(ctx context.Context, apiKey string, params map[string]string) error {
	if apiKey != "" {
		if strings.HasPrefix(apiKey, "ABSK") {
			return nil
		}
		return errors.New("not a valid bedrock api key")
	}
	var opts []func(*awsconfig.LoadOptions) error
	if region := params[ProviderParamRegion]; region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile := params[ProviderParamProfile]; profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("loading AWS configuration: %w", err)
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return fmt.Errorf("retrieving AWS credentials: %w", err)
	}
	return nil
}
