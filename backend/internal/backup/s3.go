package backup

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3Client builds the client ONLY with the target's credential (never the
// server's environment). Custom endpoint + path-style for MinIO/Ceph/RGW.
func s3Client(t *Target) *s3.Client {
	cfg := aws.Config{
		Region:      t.Region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(t.AccessKeyID, t.SecretAccessKey, "")),
	}
	if t.InsecureSkipTLS {
		cfg.HTTPClient = awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit admin option
		})
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if t.Endpoint != "" {
			o.BaseEndpoint = aws.String(t.Endpoint)
		}
		o.UsePathStyle = t.PathStyle || t.Endpoint != ""
	})
}

const s3Timeout = 60 * time.Second

// Validate checks that the bucket exists and that the credential can write to
// it (uploads and deletes a probe object under the prefix).
func Validate(ctx context.Context, t *Target) error {
	ctx, cancel := context.WithTimeout(ctx, s3Timeout)
	defer cancel()
	c := s3Client(t)
	if _, err := c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(t.Bucket)}); err != nil {
		// If it does not exist we try to create it: it is what the operator
		// would do by hand, and this way the setup leaves the target ready.
		// Without permission, the error says so.
		var nf *types.NotFound
		var nsb *types.NoSuchBucket
		if !errors.As(err, &nf) && !errors.As(err, &nsb) {
			return fmt.Errorf("cannot access bucket %q: %w", t.Bucket, redactAWS(err))
		}
		if _, cerr := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(t.Bucket)}); cerr != nil {
			return fmt.Errorf("bucket %q does not exist and could not be created: %w", t.Bucket, redactAWS(cerr))
		}
	}
	key := t.key(".bayplan-probe")
	if _, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(t.Bucket), Key: aws.String(key), Body: bytes.NewReader([]byte("ok"))}); err != nil {
		return fmt.Errorf("the credential cannot write to the bucket: %w", redactAWS(err))
	}
	_, _ = c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(t.Bucket), Key: aws.String(key)})
	return nil
}

// redactAWS strips the signed URL and the request id from the API message.
func redactAWS(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, ", RequestID:"); i > 0 {
		msg = msg[:i]
	}
	return errors.New(msg)
}

func (t *Target) key(parts ...string) string {
	all := append([]string{}, parts...)
	if t.Prefix != "" {
		all = append([]string{t.Prefix}, all...)
	}
	return strings.Join(all, "/")
}

// upload uploads the archive under the given key.
func upload(ctx context.Context, t *Target, key string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := s3Client(t).PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(t.Bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentType: aws.String("application/gzip"),
	})
	if err != nil {
		return redactAWS(err)
	}
	return nil
}

// download fetches a whole archive.
func download(ctx context.Context, t *Target, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	out, err := s3Client(t).GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(t.Bucket), Key: aws.String(key)})
	if err != nil {
		return nil, redactAWS(err)
	}
	defer out.Body.Close()
	return io.ReadAll(io.LimitReader(out.Body, maxArchiveBytes))
}

// prune deletes a schedule's oldest copies beyond the retention. It lists by
// prefix (one folder per schedule) and sorts by name: the key carries the date
// in a sortable format.
func prune(ctx context.Context, t *Target, folder string, keep int) (deleted []string, err error) {
	return pruneSuffix(ctx, t, folder, ".tar.gz", keep)
}

// pruneSuffix is prune for the objects of folder ending in suffix.
func pruneSuffix(ctx context.Context, t *Target, folder, suffix string, keep int) (deleted []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, s3Timeout)
	defer cancel()
	c := s3Client(t)
	var keys []string
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{Bucket: aws.String(t.Bucket), Prefix: aws.String(folder + "/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, redactAWS(err)
		}
		for _, o := range page.Contents {
			if strings.HasSuffix(aws.ToString(o.Key), suffix) {
				keys = append(keys, aws.ToString(o.Key))
			}
		}
	}
	sort.Strings(keys)
	if len(keys) <= keep {
		return nil, nil
	}
	for _, k := range keys[:len(keys)-keep] {
		if _, err := c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(t.Bucket), Key: aws.String(k)}); err != nil {
			return deleted, redactAWS(err)
		}
		deleted = append(deleted, k)
	}
	return deleted, nil
}
