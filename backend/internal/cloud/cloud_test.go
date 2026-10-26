package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	"k8s.io/client-go/kubernetes"
)

func TestCredentialValidate(t *testing.T) {
	cases := []struct {
		name    string
		cred    Credential
		wantErr string
	}{
		{"aws without region", Credential{Name: "a", Provider: AWS, Config: Config{AccessKeyID: "AKIA"}, Secret: Secret{SecretAccessKey: "x"}}, "region"},
		{"aws ok", Credential{Name: "a", Provider: AWS, Config: Config{AccessKeyID: "AKIA", Regions: []string{"eu-west-1"}}, Secret: Secret{SecretAccessKey: "x"}}, ""},
		{"azure without secret", Credential{Name: "a", Provider: Azure, Config: Config{TenantID: "t", ClientID: "c", SubscriptionID: "s"}}, "client secret"},
		{"gcp without json", Credential{Name: "a", Provider: GCP}, "JSON"},
		{"odd provider", Credential{Name: "a", Provider: "oci"}, "unknown"},
		{"without name", Credential{Provider: GCP, Secret: Secret{ServiceAccountJSON: "{}"}}, "name"},
	}
	for _, tc := range cases {
		err := tc.cred.Validate(true)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: expected error containing %q, got %v", tc.name, tc.wantErr, err)
		}
	}
	// An edit that does not touch the secret must not require it.
	c := Credential{Name: "a", Provider: Azure, Config: Config{TenantID: "t", ClientID: "c", SubscriptionID: "s"}}
	if err := c.Validate(false); err != nil {
		t.Errorf("Validate(false) should not require the secret: %v", err)
	}
}

func TestRefKeyDistinguishesSameNameAcrossLocations(t *testing.T) {
	a := Ref{Name: "prod", Region: "eu-west-1"}
	b := Ref{Name: "prod", Region: "us-east-1"}
	if a.Key() == b.Key() {
		t.Fatal("two clusters with the same name in different regions must have different keys")
	}
	var round Ref
	raw, _ := json.Marshal(a)
	_ = json.Unmarshal(raw, &round)
	if round.Key() != a.Key() {
		t.Fatal("the key must survive the JSON round trip (that is how it is stored in cloud_ref)")
	}
}

func TestResourceGroupOf(t *testing.T) {
	id := "/subscriptions/1234/resourceGroups/rg-prod/providers/Microsoft.ContainerService/managedClusters/aks1"
	if got := resourceGroupOf(id); got != "rg-prod" {
		t.Fatalf("got %q", got)
	}
	// ARM sometimes returns "resourcegroups" in lowercase.
	if got := resourceGroupOf(strings.ToLower(id)); got != "rg-prod" {
		t.Fatalf("lowercase: got %q", got)
	}
}

func TestJWTClaim(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"oid":"abc-123","aud":"x"}`))
	tok := "eyJhbGciOiJub25lIn0." + payload + ".sig"
	oid, err := jwtClaim(tok, "oid")
	if err != nil || oid != "abc-123" {
		t.Fatalf("got %q, %v", oid, err)
	}
	if _, err := jwtClaim(tok, "sub"); err == nil {
		t.Fatal("a missing claim must be an error")
	}
	if _, err := jwtClaim("no-jwt", "oid"); err == nil {
		t.Fatal("a token that is not a JWT must be an error")
	}
}

// The EKS token is signed locally (STS presign does not hit the network), so
// the exact format the EKS API server expects can be checked.
func TestEKSTokenFormat(t *testing.T) {
	cred := &Credential{Provider: AWS, Config: Config{AccessKeyID: "AKIAEXAMPLE"}, Secret: Secret{SecretAccessKey: "secret"}}
	src, err := awsDriver{}.TokenSource(context.Background(), cred, Ref{Name: "my-cluster", Region: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := src.Token()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.AccessToken, "k8s-aws-v1.") {
		t.Fatalf("prefix: %q", tok.AccessToken[:20])
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok.AccessToken, "k8s-aws-v1."))
	if err != nil {
		t.Fatalf("the token must be unpadded base64url: %v", err)
	}
	u, err := url.Parse(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "sts.eu-west-1.amazonaws.com" || q.Get("Action") != "GetCallerIdentity" {
		t.Fatalf("unexpected URL: %s", u)
	}
	if q.Get("X-Amz-Expires") != "60" {
		t.Fatalf("X-Amz-Expires: %q", q.Get("X-Amz-Expires"))
	}
	if !strings.Contains(q.Get("X-Amz-SignedHeaders"), "x-k8s-aws-id") {
		t.Fatalf("the x-k8s-aws-id header must be signed: %q", q.Get("X-Amz-SignedHeaders"))
	}
	if tok.Expiry.IsZero() {
		t.Fatal("the token must expire so that ReuseTokenSource renews it")
	}
}

// A key that is not a service_account (external_account points to a local
// file or a URL from which to read the credential) is rejected before touching
// anything.
func TestGCPRejectsNonServiceAccountKeys(t *testing.T) {
	ext := `{"type":"external_account","audience":"x","subject_token_type":"y","token_url":"http://127.0.0.1:1/token","credential_source":{"file":"/etc/passwd"}}`
	_, err := gcpDriver{}.credentials(context.Background(), &Credential{Secret: Secret{ServiceAccountJSON: ext}})
	if err == nil || !strings.Contains(err.Error(), "service_account") {
		t.Fatalf("expected rejection by type, got %v", err)
	}
}

// fakeDriver is used to test the transport without a real provider.
type fakeDriver struct {
	server string
	ca     []byte
	tokens int
}

func (f *fakeDriver) Validate(context.Context, *Credential) (string, error)   { return "fake", nil }
func (f *fakeDriver) List(context.Context, *Credential) ([]Discovered, error) { return nil, nil }
func (f *fakeDriver) Describe(context.Context, *Credential, Ref) (Endpoint, error) {
	return Endpoint{Server: f.server, CAData: f.ca}, nil
}
func (f *fakeDriver) TokenSource(context.Context, *Credential, Ref) (oauth2.TokenSource, error) {
	return f, nil
}

// Token implements oauth2.TokenSource counting how many times it is requested.
func (f *fakeDriver) Token() (*oauth2.Token, error) {
	f.tokens++
	return &oauth2.Token{AccessToken: "tok-" + strings.Repeat("x", f.tokens), TokenType: "Bearer"}, nil
}
func (f *fakeDriver) Subject(*Credential) (string, string) { return "User", "fake" }

// The rest.Config the package builds has to (1) put the provider's token in
// Authorization, (2) let the impersonation headers that client-go adds on the
// outside pass through intact and (3) reuse the token across requests and
// copies of the config (impersonation, Helm).
func TestRestConfigAddsProviderTokenAndKeepsImpersonation(t *testing.T) {
	var gotAuth, gotUser []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		gotUser = append(gotUser, r.Header.Get("Impersonate-User"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gitVersion":"v1.30.0"}`))
	}))
	defer srv.Close()
	ca := srv.Certificate().Raw
	pem := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(ca) + "\n-----END CERTIFICATE-----\n"

	d := &fakeDriver{server: srv.URL, ca: []byte(pem)}
	cfg, err := restConfigWith(context.Background(), d, &Credential{Provider: AWS}, Ref{Name: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := serverVersion(context.Background(), cfg); err != nil || v != "v1.30.0" {
		t.Fatalf("version: %q, %v", v, err)
	}
	// Impersonated copy, as clusterConn.impersonatedRestConfig does.
	imp := *cfg
	imp.Impersonate.UserName = "bayplan:alice"
	cs, err := kubernetes.NewForConfig(&imp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Discovery().RESTClient().Get().AbsPath("/version").Do(context.Background()).Raw(); err != nil {
		t.Fatal(err)
	}
	if len(gotAuth) != 2 || gotAuth[0] != "Bearer tok-x" || gotAuth[1] != "Bearer tok-x" {
		t.Fatalf("Authorization: %v (the token must be reused across requests and copies)", gotAuth)
	}
	if gotUser[1] != "bayplan:alice" {
		t.Fatalf("Impersonate-User must reach the server: %v", gotUser)
	}
	if d.tokens != 1 {
		t.Fatalf("the token was requested %d times, expected 1", d.tokens)
	}
}
