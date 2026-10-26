package cloud

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"golang.org/x/oauth2"
)

// awsDriver: EKS. Authentication against the API server is the one of
// aws-iam-authenticator: a PRE-SIGNED STS GetCallerIdentity (with the
// cluster's x-k8s-aws-id header) encoded as the bearer token
// "k8s-aws-v1.<base64url(url)>". The API server forwards it to STS to learn
// who the caller is and maps it to a user/groups with the access entries (or
// the aws-auth ConfigMap). It expires after 15 min; it renews itself.
type awsDriver struct{}

// eksCluster is a local alias of the SDK type so the long import is not
// repeated in signatures.
type eksCluster = ekstypes.Cluster

// awsConfig builds the SDK configuration ONLY from the stored credential: the
// server's environment and ~/.aws are not read, so that the identity used is
// exactly the one the admin registered.
func (awsDriver) awsConfig(cred *Credential, region string) aws.Config {
	return aws.Config{
		Region: region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			cred.Config.AccessKeyID, cred.Secret.SecretAccessKey, "")),
	}
}

func (d awsDriver) Validate(ctx context.Context, cred *Credential) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	out, err := sts.NewFromConfig(d.awsConfig(cred, cred.Config.Regions[0])).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("AWS rejects the credential: %w", err)
	}
	return aws.ToString(out.Arn), nil
}

func (d awsDriver) List(ctx context.Context, cred *Credential) ([]Discovered, error) {
	out := []Discovered{}
	for _, region := range cred.Config.Regions {
		client := eks.NewFromConfig(d.awsConfig(cred, region))
		names, err := d.listNames(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("EKS %s: %w", region, err)
		}
		for _, name := range names {
			c, err := d.describe(ctx, client, name)
			if err != nil {
				return nil, fmt.Errorf("EKS %s/%s: %w", region, name, err)
			}
			out = append(out, Discovered{
				Ref:      Ref{Name: name, Region: region},
				Name:     name,
				Location: region,
				Version:  aws.ToString(c.Version),
				Status:   string(c.Status),
				Endpoint: aws.ToString(c.Endpoint),
				Private:  c.ResourcesVpcConfig != nil && !c.ResourcesVpcConfig.EndpointPublicAccess,
			})
		}
	}
	return out, nil
}

func (awsDriver) listNames(ctx context.Context, client *eks.Client) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	var names []string
	p := eks.NewListClustersPaginator(client, &eks.ListClustersInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		names = append(names, page.Clusters...)
	}
	return names, nil
}

func (awsDriver) describe(ctx context.Context, client *eks.Client, name string) (*eksCluster, error) {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	out, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
	if err != nil {
		return nil, err
	}
	if out.Cluster == nil {
		return nil, errors.New("response without cluster")
	}
	return out.Cluster, nil
}

func (d awsDriver) Describe(ctx context.Context, cred *Credential, ref Ref) (Endpoint, error) {
	if ref.Region == "" || ref.Name == "" {
		return Endpoint{}, errors.New("EKS: the reference needs region and name")
	}
	c, err := d.describe(ctx, eks.NewFromConfig(d.awsConfig(cred, ref.Region)), ref.Name)
	if err != nil {
		return Endpoint{}, fmt.Errorf("EKS %s/%s: %w", ref.Region, ref.Name, err)
	}
	if c.Endpoint == nil || c.CertificateAuthority == nil || c.CertificateAuthority.Data == nil {
		return Endpoint{}, fmt.Errorf("EKS %s/%s: the cluster has no endpoint yet (status %s)", ref.Region, ref.Name, c.Status)
	}
	ca, err := base64.StdEncoding.DecodeString(aws.ToString(c.CertificateAuthority.Data))
	if err != nil {
		return Endpoint{}, fmt.Errorf("EKS: invalid CA: %w", err)
	}
	return Endpoint{Server: aws.ToString(c.Endpoint), CAData: ca}, nil
}

func (d awsDriver) TokenSource(_ context.Context, cred *Credential, ref Ref) (oauth2.TokenSource, error) {
	return &eksTokenSource{cfg: d.awsConfig(cred, ref.Region), cluster: ref.Name}, nil
}

// Subject: on EKS the IAM principal is not a name Kubernetes knows; the access
// entry (or aws-auth) maps it to groups, and the RBAC is granted to the group.
func (awsDriver) Subject(*Credential) (string, string) {
	return "Group", "bayplan:cloud"
}

// eksTokenSource issues the pre-signed token. It is valid for 15 min from
// signing; it is cached for 10 (oauth2.ReuseTokenSource renews on expiry).
type eksTokenSource struct {
	cfg     aws.Config
	cluster string
}

func (s *eksTokenSource) Token() (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()
	presign := sts.NewPresignClient(sts.NewFromConfig(s.cfg))
	req, err := presign.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(po *sts.PresignOptions) {
		po.ClientOptions = append(po.ClientOptions, func(o *sts.Options) {
			o.APIOptions = append(o.APIOptions,
				smithyhttp.SetHeaderValue("x-k8s-aws-id", s.cluster),
				smithyhttp.SetHeaderValue("X-Amz-Expires", "60"),
			)
		})
	})
	if err != nil {
		return nil, fmt.Errorf("EKS: failed signing the STS token: %w", err)
	}
	return &oauth2.Token{
		AccessToken: "k8s-aws-v1." + base64.RawURLEncoding.EncodeToString([]byte(req.URL)),
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(10 * time.Minute),
	}, nil
}
