package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
	"path/filepath"
)

// The push configuration, contract pharos-push-config/1
// (contracts/plugins/push.schema.json, "config").

const pushConfigContract = "pharos-push-config/1"

type pushConfig struct {
	Contract      string          `json:"contract"`
	Access        string          `json:"access"`
	Prefix        string          `json:"prefix,omitempty"`
	RetentionDays int             `json:"retention_days,omitempty"`
	Pool          string          `json:"pool,omitempty"`
	Owner         string          `json:"owner,omitempty"`
	Automation    string          `json:"automation,omitempty"`
	Sources       []pushSource    `json:"sources,omitempty"`
	Destination   pushDestination `json:"destination"`
}

// pushSource names a folder to capture. Without any, capture-push looks in
// the usual folders of the kinds it can push.
type pushSource struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type pushDestination struct {
	Type string `json:"type"`
	// s3, and gcs with an HMAC key.
	Bucket          string     `json:"bucket,omitempty"`
	Region          string     `json:"region,omitempty"`
	Endpoint        string     `json:"endpoint,omitempty"`
	AccessKeyID     string     `json:"access_key_id,omitempty"`
	SecretAccessKey pushSecret `json:"secret_access_key,omitempty"`
	// Keyless: a signed identity token traded for short-lived access.
	IdentityToken *pushIdentityToken `json:"identity_token,omitempty"`
	RoleARN       string             `json:"role_arn,omitempty"`
	// gcs.
	ServiceAccountKey        pushSecret `json:"service_account_key,omitempty"`
	WorkloadIdentityProvider string     `json:"workload_identity_provider,omitempty"`
	ServiceAccount           string     `json:"service_account,omitempty"`
	// AccessToken supplies a Google access token directly, for a machine
	// already signed in by other means.
	AccessToken *pushIdentityToken `json:"access_token,omitempty"`
	// azure.
	SASURL    pushSecret `json:"sas_url,omitempty"`
	Account   string     `json:"account,omitempty"`
	Container string     `json:"container,omitempty"`
	TenantID  string     `json:"tenant_id,omitempty"`
	ClientID  string     `json:"client_id,omitempty"`
	// http.
	URLTemplate string     `json:"url_template,omitempty"`
	Header      pushSecret `json:"header,omitempty"`
}

// pushSecret is a secret given inline, or named by an environment variable
// or a file so that it can live in a secret store. It never prints itself.
type pushSecret struct{ value, env, file string }

func (s *pushSecret) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		if text == "" {
			return errors.New("a secret is empty")
		}
		s.value = text
		return nil
	}
	var named struct{ Env, File string }
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&named); err != nil || (named.Env == "") == (named.File == "") {
		return errors.New(`a secret is a string, {"env": NAME} or {"file": PATH}`)
	}
	s.env, s.file = named.Env, named.File
	return nil
}

func (s pushSecret) MarshalJSON() ([]byte, error) { return []byte(`"…"`), nil }
func (s pushSecret) String() string               { return "…" }
func (s pushSecret) set() bool                    { return s.value != "" || s.env != "" || s.file != "" }

// read resolves the secret; base is the folder relative file names are in.
func (s pushSecret) read(base string) (string, error) {
	switch {
	case s.value != "":
		return s.value, nil
	case s.env != "":
		if value := os.Getenv(s.env); value != "" {
			return value, nil
		}
		return "", fmt.Errorf("environment variable %s is not set", s.env)
	case s.file != "":
		data, err := os.ReadFile(pushPath(base, s.file))
		if err != nil {
			return "", err
		}
		if value := strings.TrimSpace(string(data)); value != "" {
			return value, nil
		}
		return "", fmt.Errorf("%s is empty", s.file)
	}
	return "", errors.New("no secret is configured")
}

func pushPath(base, name string) string {
	name = expandPath(name)
	if base != "" && !strings.HasPrefix(name, "/") {
		return base + "/" + name
	}
	return name
}

// pushIdentityToken says where this machine's signed identity token (OIDC)
// comes from. Such tokens live minutes, so one is fetched whenever access
// has to be renewed.
type pushIdentityToken struct {
	Command []string `json:"command,omitempty"`
	Env     string   `json:"env,omitempty"`
	File    string   `json:"file,omitempty"`
}

func (t *pushIdentityToken) read(ctx context.Context, base string) (string, error) {
	var token string
	switch {
	case t == nil:
		return "", errors.New("no identity token is configured")
	case len(t.Command) > 0:
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, pushCommandPath(t.Command[0]), t.Command[1:]...).Output()
		if err != nil {
			return "", fmt.Errorf("%s did not provide a token: %w", t.Command[0], err)
		}
		token = string(output)
	case t.Env != "":
		token = os.Getenv(t.Env)
	case t.File != "":
		data, err := os.ReadFile(pushPath(base, t.File))
		if err != nil {
			return "", err
		}
		token = string(data)
	}
	if token = strings.TrimSpace(token); token == "" {
		return "", errors.New("the identity token is empty")
	}
	return token, nil
}

// pushCommandPath finds a credential command the way Terminal would. The Mac
// app inherits launchd's PATH, which lacks Homebrew and the cloud SDKs, so a
// bare name this process cannot find is looked up on the login shell's PATH.
func pushCommandPath(name string) string {
	if strings.ContainsRune(name, filepath.Separator) {
		return name
	}
	if _, err := exec.LookPath(name); err == nil {
		return name
	}
	values, _ := cachedLoginShellEnv([]string{"PATH"})
	for _, dir := range filepath.SplitList(values["PATH"]) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return name
}

// readPushConfig reads and checks a push configuration.
func readPushConfig(file string) (pushConfig, error) {
	var config pushConfig
	data, err := os.ReadFile(file)
	if err != nil {
		return config, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("%s: %w", file, err)
	}
	return config, config.validate()
}

func (c pushConfig) validate() error {
	if c.Contract != pushConfigContract {
		return fmt.Errorf("the push configuration's contract is %q, not %s", c.Contract, pushConfigContract)
	}
	if c.Access != "write" && c.Access != "read" {
		return errors.New(`the push configuration's access must be "write" or "read"`)
	}
	if strings.HasPrefix(c.Prefix, "/") || strings.HasSuffix(c.Prefix, "/") {
		return errors.New("the prefix must not begin or end with a slash")
	}
	if c.RetentionDays < 0 {
		return errors.New("retention_days cannot be negative")
	}
	for _, source := range c.Sources {
		spec, ok := providerFor(source.Kind)
		if !validCaptureName(source.Name) || source.Path == "" || !ok || spec.Import == nil || !slices.Contains(spec.Import.Forms, importCaptureExport) {
			return fmt.Errorf("source %q must have a name, a path and a kind that can be pushed", source.Name)
		}
	}
	if c.Owner != "" && !pushOwnerPattern.MatchString(c.Owner) {
		return fmt.Errorf("%q cannot name an owner", c.Owner)
	}
	d := c.Destination
	stored := d.AccessKeyID != "" && d.SecretAccessKey.set()
	switch d.Type {
	case "s3":
		if d.Bucket == "" || stored == (d.RoleARN != "" && d.IdentityToken != nil) {
			return errors.New("an s3 destination needs a bucket and either an access key or a role with an identity token")
		}
	case "gcs":
		ways := 0
		for _, way := range []bool{stored, d.ServiceAccountKey.set(), d.WorkloadIdentityProvider != "" && d.IdentityToken != nil, d.AccessToken != nil} {
			if way {
				ways++
			}
		}
		if d.Bucket == "" || ways != 1 {
			return errors.New("a gcs destination needs a bucket and one of an HMAC key, a service account key, an access token, or a workload identity provider with an identity token")
		}
	case "azure":
		keyless := d.Account != "" && d.Container != "" && d.TenantID != "" && d.ClientID != "" && d.IdentityToken != nil
		if d.SASURL.set() == keyless {
			return errors.New("an azure destination needs either a SAS URL, or an account, container, tenant, client and identity token")
		}
	case "http":
		if !strings.HasPrefix(d.URLTemplate, "https://") || !strings.Contains(d.URLTemplate, "{key}") {
			return errors.New("an http destination needs an https url_template containing {key}")
		}
		if c.Access != "write" {
			return errors.New("an http destination can be pushed to but not read from")
		}
	default:
		return fmt.Errorf("unknown destination type %q", d.Type)
	}
	return nil
}

// pushStore writes objects under a destination. Every implementation sends
// plain HTTPS from this process: no cloud vendor's CLI is involved.
type pushStore interface {
	put(ctx context.Context, key string, body []byte) error
}

var (
	// pushHTTP carries every push request; tests replace it.
	pushHTTP = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// pushRetryDelay separates the attempts at one request.
	pushRetryDelay = time.Second
)

const pushAttempts = 3

// errPushRefused marks an answer that retrying will not change, such as a
// rejected credential.
var errPushRefused = errors.New("the destination refused the request")

// pushDo sends one request, built anew for each attempt, and returns its
// body when the status is one of ok.
func pushDo(ctx context.Context, build func() (*http.Request, error), ok ...int) ([]byte, error) {
	var last error
	for attempt := 0; attempt < pushAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(pushRetryDelay):
			}
		}
		request, err := build()
		if err != nil {
			return nil, err
		}
		response, err := pushHTTP.Do(request.WithContext(ctx))
		if err != nil {
			last = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, pushObjectLimit))
		response.Body.Close()
		if err != nil {
			last = err
			continue
		}
		for _, status := range ok {
			if response.StatusCode == status {
				return body, nil
			}
		}
		last = fmt.Errorf("%s answered %s: %s", request.URL.Host, response.Status, strings.TrimSpace(string(body[:min(len(body), 300)])))
		if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusRequestTimeout {
			return nil, fmt.Errorf("%w: %v", errPushRefused, last)
		}
	}
	return nil, last
}

// pushRefused reports whether err is the destination saying no, which a
// retry will not change, as against a network that could not be reached.
func pushRefused(err error) bool {
	var response *awshttp.ResponseError
	if errors.As(err, &response) {
		status := response.HTTPStatusCode()
		return status >= 400 && status < 500 && status != http.StatusTooManyRequests && status != http.StatusRequestTimeout
	}
	return errors.Is(err, errPushRefused)
}

func newPushStore(config pushConfig, base string) (pushStore, error) {
	d := config.Destination
	switch d.Type {
	case "s3":
		return newPushS3(d, base)
	case "gcs":
		if d.AccessKeyID != "" {
			// An HMAC key signs as S3 does, against Cloud Storage's XML API.
			d.Endpoint, d.Region = defaultString(d.Endpoint, "https://storage.googleapis.com"), defaultString(d.Region, "auto")
			return newPushS3(d, base)
		}
		return &pushGCS{d: d, base: base}, nil
	case "azure":
		return &pushAzure{d: d, base: base}, nil
	case "http":
		return &pushPlain{d: d, base: base}, nil
	}
	return nil, fmt.Errorf("unknown destination type %q", d.Type)
}

// S3 and S3-compatible stores, through the AWS SDK's signer.

type pushS3 struct {
	client *s3.Client
	bucket string
}

// pushSTSEndpoint is where an identity token is traded for AWS credentials.
var pushSTSEndpoint = "https://sts.amazonaws.com/"

func newPushS3(d pushDestination, base string) (*pushS3, error) {
	var provider aws.CredentialsProvider
	if d.RoleARN != "" {
		provider = aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return pushAssumeRole(ctx, d, base)
		}))
	} else {
		secret, err := d.SecretAccessKey.read(base)
		if err != nil {
			return nil, err
		}
		provider = credentials.NewStaticCredentialsProvider(d.AccessKeyID, secret, "")
	}
	options := s3.Options{
		Region: defaultString(d.Region, "us-east-1"), Credentials: provider, HTTPClient: pushHTTP, RetryMaxAttempts: pushAttempts,
		// Stores other than S3 itself do not accept trailing checksums.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	}
	if d.Endpoint != "" {
		options.BaseEndpoint, options.UsePathStyle = aws.String(d.Endpoint), true
	}
	return &pushS3{client: s3.New(options), bucket: d.Bucket}, nil
}

// pushAssumeRole is AssumeRoleWithWebIdentity, which needs no signature.
func pushAssumeRole(ctx context.Context, d pushDestination, base string) (aws.Credentials, error) {
	token, err := d.IdentityToken.read(ctx, base)
	if err != nil {
		return aws.Credentials{}, err
	}
	form := url.Values{"Action": {"AssumeRoleWithWebIdentity"}, "Version": {"2011-06-15"},
		"RoleArn": {d.RoleARN}, "RoleSessionName": {"pharos-capture-push"}, "WebIdentityToken": {token}}
	body, err := pushDo(ctx, func() (*http.Request, error) {
		request, err := http.NewRequest(http.MethodPost, pushSTSEndpoint, strings.NewReader(form.Encode()))
		if err == nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return request, err
	}, http.StatusOK)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("trading the identity token for AWS access: %w", err)
	}
	var answer struct {
		Result struct {
			Credentials struct {
				AccessKeyId, SecretAccessKey, SessionToken string
				Expiration                                 time.Time
			}
		} `xml:"AssumeRoleWithWebIdentityResult"`
	}
	if err := xml.Unmarshal(body, &answer); err != nil || answer.Result.Credentials.AccessKeyId == "" {
		return aws.Credentials{}, errors.New("AWS answered the identity token without credentials")
	}
	c := answer.Result.Credentials
	return aws.Credentials{AccessKeyID: c.AccessKeyId, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken,
		CanExpire: !c.Expiration.IsZero(), Expires: c.Expiration, Source: "pharos identity token"}, nil
}

func (s *pushS3) put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key),
		Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body)))})
	return err
}

// pushBearer keeps one short-lived access token and renews it a minute
// before it ends.
type pushBearer struct {
	mu      sync.Mutex
	token   string
	expires time.Time
	renew   func(ctx context.Context) (token string, expires time.Time, err error)
}

func (b *pushBearer) get(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token != "" && time.Until(b.expires) > time.Minute {
		return b.token, nil
	}
	token, expires, err := b.renew(ctx)
	if err != nil {
		return "", err
	}
	b.token, b.expires = token, expires
	return token, nil
}

// Google Cloud Storage with a service account key or workload identity.

type pushGCS struct {
	d      pushDestination
	base   string
	bearer *pushBearer
	once   sync.Once
	err    error
}

const (
	pushGCSScope  = "https://www.googleapis.com/auth/devstorage.read_write"
	pushGCSObject = "https://storage.googleapis.com/storage/v1/b/%s/o/%s?alt=media"
	pushGCSList   = "https://storage.googleapis.com/storage/v1/b/%s/o?prefix=%s&fields=items/name,nextPageToken&pageToken=%s"
	// Endpoints, as variables only so that their hosts are in one place.
	pushGCSUpload      = "https://storage.googleapis.com/upload/storage/v1/b/%s/o?uploadType=media&name=%s"
	pushGoogleSTS      = "https://sts.googleapis.com/v1/token"
	pushGoogleIAM      = "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/%s:generateAccessToken"
	pushGoogleTokenURL = "https://oauth2.googleapis.com/token"
)

func (g *pushGCS) prepare() error {
	g.once.Do(func() {
		if g.d.AccessToken != nil {
			// Whatever made the token knows when it ends; ask again soon.
			g.bearer = &pushBearer{renew: func(ctx context.Context) (string, time.Time, error) {
				token, err := g.d.AccessToken.read(ctx, g.base)
				return token, time.Now().Add(5 * time.Minute), err
			}}
			return
		}
		if !g.d.ServiceAccountKey.set() {
			g.bearer = &pushBearer{renew: g.federate}
			return
		}
		raw, err := g.d.ServiceAccountKey.read(g.base)
		if err != nil {
			g.err = err
			return
		}
		var key struct {
			ClientEmail string `json:"client_email"`
			PrivateKey  string `json:"private_key"`
			TokenURI    string `json:"token_uri"`
		}
		if err := json.Unmarshal([]byte(raw), &key); err != nil || key.ClientEmail == "" || key.PrivateKey == "" {
			g.err = errors.New("the service account key is not a Google service account JSON key")
			return
		}
		source := (&jwt.Config{Email: key.ClientEmail, PrivateKey: []byte(key.PrivateKey), Scopes: []string{pushGCSScope},
			TokenURL: pushGoogleTokenURL}).TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, pushHTTP))
		g.bearer = &pushBearer{renew: func(context.Context) (string, time.Time, error) {
			token, err := source.Token()
			if err != nil {
				return "", time.Time{}, err
			}
			return token.AccessToken, token.Expiry, nil
		}}
	})
	return g.err
}

// federate trades the identity token for a Google access token, and that
// for the service account's when one is named.
func (g *pushGCS) federate(ctx context.Context) (string, time.Time, error) {
	subject, err := g.d.IdentityToken.read(ctx, g.base)
	if err != nil {
		return "", time.Time{}, err
	}
	scope := pushGCSScope
	if g.d.ServiceAccount != "" {
		scope = "https://www.googleapis.com/auth/cloud-platform"
	}
	var federated struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := pushJSON(ctx, pushGoogleSTS, "", map[string]any{
		"grantType": "urn:ietf:params:oauth:grant-type:token-exchange", "audience": "//iam.googleapis.com/" + strings.TrimPrefix(g.d.WorkloadIdentityProvider, "//iam.googleapis.com/"),
		"scope": scope, "requestedTokenType": "urn:ietf:params:oauth:token-type:access_token",
		"subjectToken": subject, "subjectTokenType": "urn:ietf:params:oauth:token-type:jwt"}, &federated); err != nil || federated.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("trading the identity token for Google Cloud access: %w", errors.Join(err, errPushRefused))
	}
	expires := time.Now().Add(time.Duration(federated.ExpiresIn) * time.Second)
	if g.d.ServiceAccount == "" {
		return federated.AccessToken, expires, nil
	}
	var impersonated struct {
		AccessToken string    `json:"accessToken"`
		ExpireTime  time.Time `json:"expireTime"`
	}
	if err := pushJSON(ctx, fmt.Sprintf(pushGoogleIAM, url.PathEscape(g.d.ServiceAccount)), federated.AccessToken,
		map[string]any{"scope": []string{pushGCSScope}}, &impersonated); err != nil || impersonated.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("acting as %s: %w", g.d.ServiceAccount, errors.Join(err, errPushRefused))
	}
	return impersonated.AccessToken, impersonated.ExpireTime, nil
}

// pushJSON posts a JSON body, with a bearer token when given, and decodes
// the JSON answer.
func pushJSON(ctx context.Context, endpoint, bearer string, body, answer any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	data, err := pushDo(ctx, func() (*http.Request, error) {
		request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
			if bearer != "" {
				request.Header.Set("Authorization", "Bearer "+bearer)
			}
		}
		return request, err
	}, http.StatusOK)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, answer)
}

func (g *pushGCS) put(ctx context.Context, key string, body []byte) error {
	if err := g.prepare(); err != nil {
		return err
	}
	token, err := g.bearer.get(ctx)
	if err != nil {
		return err
	}
	_, err = pushDo(ctx, func() (*http.Request, error) {
		request, err := http.NewRequest(http.MethodPost, fmt.Sprintf(pushGCSUpload, url.PathEscape(g.d.Bucket), url.QueryEscape(key)), bytes.NewReader(body))
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/octet-stream")
		}
		return request, err
	}, http.StatusOK)
	return err
}

// Azure Blob Storage with a SAS URL or a federated credential.

type pushAzure struct {
	d      pushDestination
	base   string
	bearer *pushBearer
	once   sync.Once
}

const (
	pushAzureVersion = "2023-11-03"
	pushAzureLogin   = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"
)

func (a *pushAzure) put(ctx context.Context, key string, body []byte) error {
	target, bearer := "", ""
	if a.d.SASURL.set() {
		raw, err := a.d.SASURL.read(a.base)
		if err != nil {
			return err
		}
		sas, err := url.Parse(raw)
		if err != nil || sas.Scheme != "https" || sas.RawQuery == "" {
			return errors.New("the SAS URL is not an https URL with a signature")
		}
		sas.Path = strings.TrimSuffix(sas.Path, "/") + "/" + key
		target = sas.String()
	} else {
		a.once.Do(func() { a.bearer = &pushBearer{renew: a.federate} })
		var err error
		if bearer, err = a.bearer.get(ctx); err != nil {
			return err
		}
		target = (&url.URL{Scheme: "https", Host: a.d.Account + ".blob.core.windows.net", Path: "/" + a.d.Container + "/" + key}).String()
	}
	_, err := pushDo(ctx, func() (*http.Request, error) {
		request, err := http.NewRequest(http.MethodPut, target, bytes.NewReader(body))
		if err == nil {
			request.Header.Set("x-ms-blob-type", "BlockBlob")
			request.Header.Set("x-ms-version", pushAzureVersion)
			if bearer != "" {
				request.Header.Set("Authorization", "Bearer "+bearer)
			}
		}
		return request, err
	}, http.StatusCreated)
	return err
}

func (a *pushAzure) federate(ctx context.Context) (string, time.Time, error) {
	assertion, err := a.d.IdentityToken.read(ctx, a.base)
	if err != nil {
		return "", time.Time{}, err
	}
	form := url.Values{"client_id": {a.d.ClientID}, "scope": {"https://storage.azure.com/.default"}, "grant_type": {"client_credentials"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}, "client_assertion": {assertion}}
	body, err := pushDo(ctx, func() (*http.Request, error) {
		request, err := http.NewRequest(http.MethodPost, fmt.Sprintf(pushAzureLogin, url.PathEscape(a.d.TenantID)), strings.NewReader(form.Encode()))
		if err == nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return request, err
	}, http.StatusOK)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("trading the identity token for Azure access: %w", err)
	}
	var answer struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.AccessToken == "" {
		return "", time.Time{}, errors.New("Azure answered the identity token without access")
	}
	return answer.AccessToken, time.Now().Add(time.Duration(answer.ExpiresIn) * time.Second), nil
}

// Anything that accepts an HTTPS PUT.

type pushPlain struct {
	d    pushDestination
	base string
}

func (p *pushPlain) put(ctx context.Context, key string, body []byte) error {
	name, value := "", ""
	if p.d.Header.set() {
		header, err := p.d.Header.read(p.base)
		if err != nil {
			return err
		}
		var ok bool
		if name, value, ok = strings.Cut(header, ":"); !ok || strings.TrimSpace(name) == "" {
			return errors.New(`the header must read "Name: value"`)
		}
	}
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	target := strings.ReplaceAll(p.d.URLTemplate, "{key}", strings.Join(segments, "/"))
	_, err := pushDo(ctx, func() (*http.Request, error) {
		request, err := http.NewRequest(http.MethodPut, target, bytes.NewReader(body))
		if err == nil && name != "" {
			request.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
		}
		return request, err
	}, http.StatusOK, http.StatusCreated, http.StatusNoContent)
	return err
}

// Reading, for the library that imports what was pushed.

// pushReadStore lists and fetches objects. A reader's credential may read
// and list and need not write.
type pushReadStore interface {
	// list returns every key beginning with prefix, in order.
	list(ctx context.Context, prefix string) ([]string, error)
	get(ctx context.Context, key string) ([]byte, error)
}

func newPushReadStore(config pushConfig, base string) (pushReadStore, error) {
	store, err := newPushStore(config, base)
	if err != nil {
		return nil, err
	}
	if reader, ok := store.(pushReadStore); ok {
		return reader, nil
	}
	return nil, fmt.Errorf("a %s destination cannot be imported from yet", config.Destination.Type)
}

// pushObjectLimit bounds one fetched object: a chunk is at most 256 KiB and
// an export lists a capture's members.
const pushObjectLimit = 256 << 20

func (g *pushGCS) authorized(ctx context.Context, method, target string) ([]byte, error) {
	if err := g.prepare(); err != nil {
		return nil, err
	}
	token, err := g.bearer.get(ctx)
	if err != nil {
		return nil, err
	}
	return pushDo(ctx, func() (*http.Request, error) {
		request, err := http.NewRequest(method, target, nil)
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request, err
	}, http.StatusOK)
}

func (g *pushGCS) get(ctx context.Context, key string) ([]byte, error) {
	return g.authorized(ctx, http.MethodGet, fmt.Sprintf(pushGCSObject, url.PathEscape(g.d.Bucket), url.PathEscape(key)))
}

func (g *pushGCS) list(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for page := ""; ; {
		body, err := g.authorized(ctx, http.MethodGet, fmt.Sprintf(pushGCSList, url.PathEscape(g.d.Bucket), url.QueryEscape(prefix), url.QueryEscape(page)))
		if err != nil {
			return nil, err
		}
		var answer struct {
			Items         []struct{ Name string }
			NextPageToken string
		}
		if err := json.Unmarshal(body, &answer); err != nil {
			return nil, err
		}
		for _, item := range answer.Items {
			keys = append(keys, item.Name)
		}
		if page = answer.NextPageToken; page == "" {
			slices.Sort(keys)
			return keys, nil
		}
	}
}

func (s *pushS3) get(ctx context.Context, key string) ([]byte, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()
	return io.ReadAll(io.LimitReader(object.Body, pushObjectLimit))
}

func (s *pushS3) list(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, object := range page.Contents {
			keys = append(keys, aws.ToString(object.Key))
		}
	}
	slices.Sort(keys)
	return keys, nil
}
